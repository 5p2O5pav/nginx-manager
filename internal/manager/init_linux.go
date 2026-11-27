//go:build linux

package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"nginx-manager/internal/config"
	"nginx-manager/internal/nginx"
)

const initJournal = root + "/init-transaction.json"

type initSnapshot struct {
	Nginx   []byte `json:"nginx"`
	Running bool   `json:"running"`
}

func recoverInit() error {
	b, err := os.ReadFile(initJournal)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var s initSnapshot
	if err := json.Unmarshal(b, &s); err != nil || len(s.Nginx) == 0 {
		return fmt.Errorf("初始化恢复日志损坏: %s", initJournal)
	}
	if err := atomicWrite(nginxFile, s.Nginx, 0644); err != nil {
		return err
	}
	if s.Running {
		if err := command("systemctl", "restart", "nginx"); err != nil {
			return fmt.Errorf("原配置已恢复但服务恢复失败: %w", err)
		}
	} else {
		if err := command("systemctl", "stop", "nginx"); err != nil {
			return err
		}
	}
	return os.Remove(initJournal)
}

// Initialize takes over only after the caller has obtained explicit consent.
// It preserves the old Nginx tree before changing the main configuration.
func Initialize() error {
	if os.Geteuid() != 0 {
		return errors.New("请使用 root 运行")
	}
	if _, err := os.Stat(configFile); err == nil {
		return errors.New("已经初始化")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := exec.LookPath("nginx"); err != nil {
		return errors.New("未找到 Nginx，请先安装")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(root+"/lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lockFile(lock); err != nil {
		return err
	}
	defer unlockFile(lock)
	if _, err := os.Stat(configFile); err == nil {
		return errors.New("已经初始化")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := recoverInit(); err != nil {
		return fmt.Errorf("恢复中断的初始化失败: %w", err)
	}
	backup := filepath.Join(root, "nginx-original-backup")
	if info, err := os.Stat(backup); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("备份路径不是目录: %s", backup)
		}
		if _, err := os.Stat(filepath.Join(root, ".original-backup-complete")); err != nil {
			return fmt.Errorf("已有备份未标记为完整，请检查 %s: %w", backup, err)
		}
		// Preserve the verified original backup across a retry; never replace it.
	} else if errors.Is(err, os.ErrNotExist) {
		tmp := filepath.Join(root, ".nginx-original-backup-pending")
		if _, err := os.Lstat(tmp); err == nil {
			return fmt.Errorf("发现未完成的 Nginx 备份 %s，请检查并移走后重试", tmp)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := command("cp", "-a", "/etc/nginx", tmp); err != nil {
			return fmt.Errorf("备份 Nginx 配置失败: %w", err)
		}
		if err := os.Rename(tmp, backup); err != nil {
			return fmt.Errorf("保存 Nginx 备份失败: %w", err)
		}
		if err := atomicWrite(filepath.Join(root, ".original-backup-complete"), []byte("complete\n"), 0600); err != nil {
			return err
		}
	} else {
		return err
	}
	old, err := os.ReadFile(nginxFile)
	if err != nil {
		return err
	}
	for _, dir := range []string{"/etc/nginx/ssl", paths.Webroot, paths.DefaultDir, root + "/backup"} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	if err := os.Chmod(root+"/backup", 0700); err != nil {
		return err
	}
	page := []byte("<!doctype html><html><head><meta charset=\"utf-8\"><title>Nginx Manager</title></head><body><h1>Nginx Manager</h1><p>It works</p></body></html>\n")
	if err := atomicWrite(filepath.Join(paths.DefaultDir, "index.html"), page, 0644); err != nil {
		return err
	}
	c := config.Config{Version: 1, Domains: []config.Domain{}}
	text, err := nginx.Render(c, paths, time.Now())
	if err != nil {
		return err
	}
	f, err := os.CreateTemp("/etc/nginx", ".nm-init-*.conf")
	if err != nil {
		return err
	}
	candidate := f.Name()
	defer os.Remove(candidate)
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := command("nginx", "-t", "-c", candidate); err != nil {
		return err
	}
	s := initSnapshot{Nginx: old, Running: exec.Command("systemctl", "is-active", "--quiet", "nginx").Run() == nil}
	journal, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := atomicWrite(initJournal, journal, 0600); err != nil {
		return err
	}
	rollback := func(cause error) error {
		if err := os.Remove(configFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("初始化失败: %v；无法清理配置文件: %w", cause, err)
		}
		if err := recoverInit(); err != nil {
			return fmt.Errorf("初始化失败: %v；恢复原配置失败: %w", cause, err)
		}
		return fmt.Errorf("初始化失败，原配置已恢复: %w", cause)
	}
	if err := atomicWrite(nginxFile, []byte(text), 0644); err != nil {
		return rollback(err)
	}
	if err := command("systemctl", "restart", "nginx"); err != nil {
		return rollback(err)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return rollback(err)
	}
	if err := atomicWrite(configFile, append(b, '\n'), 0600); err != nil {
		return rollback(err)
	}
	if err := os.Remove(initJournal); err != nil {
		return fmt.Errorf("初始化已完成，但无法清理初始化日志: %w", err)
	}
	return nil
}
