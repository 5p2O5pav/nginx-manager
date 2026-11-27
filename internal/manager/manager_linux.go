//go:build linux

// Package manager coordinates configuration updates on Linux.
package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"nginx-manager/internal/config"
	"nginx-manager/internal/nginx"
)

const (
	root        = "/etc/nm"
	configFile  = root + "/config.json"
	nginxFile   = "/etc/nginx/nginx.conf"
	journalFile = root + "/transaction.json"
)

var paths = nginx.Paths{SSLDir: "/etc/nginx/ssl", Webroot: "/var/www/acme", DefaultDir: "/var/www/nm/default", CABundle: "/etc/ssl/certs/ca-certificates.crt"}

type Manager struct{ lock *os.File }
type snapshot struct {
	Config []byte      `json:"config"`
	Nginx  []byte      `json:"nginx"`
	Files  []savedFile `json:"files,omitempty"`
}

type savedFile struct {
	Path   string      `json:"path"`
	Data   []byte      `json:"data,omitempty"`
	Exists bool        `json:"exists"`
	Mode   os.FileMode `json:"mode"`
}
type fileChange struct {
	Path   string
	Data   []byte
	Mode   os.FileMode
	Delete bool
}

func lockFile(f *os.File) error   { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }
func unlockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

func Open() (*Manager, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("请使用 root 运行")
	}
	if _, err := os.Stat(configFile); err != nil {
		return nil, fmt.Errorf("项目尚未初始化，请先运行 nm: %w", err)
	}
	f, err := os.OpenFile(root+"/lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	m := &Manager{lock: f}
	// A present config.json is the commit marker for initialization.
	if err := os.Remove(initJournal); err != nil && !errors.Is(err, os.ErrNotExist) {
		m.Close()
		return nil, fmt.Errorf("无法清理初始化日志: %w", err)
	}
	if err := m.recover(); err != nil {
		m.Close()
		return nil, err
	}
	return m, nil
}

func (m *Manager) Close() { syscall.Flock(int(m.lock.Fd()), syscall.LOCK_UN); m.lock.Close() }

func (m *Manager) Read() (config.Config, error) {
	var c config.Config
	b, err := os.ReadFile(configFile)
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, c.Normalize()
}

func command(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s 失败: %w\n%s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func atomicWrite(name string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(name), ".nm-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, name); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (m *Manager) recover() error {
	b, err := os.ReadFile(journalFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var s snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("事务日志损坏，请人工检查 %s: %w", journalFile, err)
	}
	if err := m.restore(s); err != nil {
		return fmt.Errorf("恢复未完成事务失败: %w", err)
	}
	return os.Remove(journalFile)
}

func (m *Manager) restore(s snapshot) error {
	for _, f := range s.Files {
		if f.Exists {
			if err := atomicWrite(f.Path, f.Data, f.Mode); err != nil {
				return err
			}
		} else if err := os.Remove(f.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := atomicWrite(configFile, s.Config, 0600); err != nil {
		return err
	}
	if err := atomicWrite(nginxFile, s.Nginx, 0644); err != nil {
		return err
	}
	if err := command("nginx", "-t", "-c", nginxFile); err != nil {
		return err
	}
	return command("systemctl", "reload", "nginx")
}

func (m *Manager) Validate(c config.Config) (string, error) {
	text, err := nginx.Render(c, paths, time.Now())
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("/etc/nginx", ".nm-candidate-*.conf")
	if err != nil {
		return "", err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.WriteString(text); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if err = command("nginx", "-t", "-c", name); err != nil {
		return "", err
	}
	return text, nil
}

func (m *Manager) Backup() (string, error) {
	b, err := os.ReadFile(configFile)
	if err != nil {
		return "", err
	}
	dir := root + "/backup"
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	name := filepath.Join(dir, time.Now().UTC().Format("20060102T150405.000000000")+".json")
	if err := atomicWrite(name, b, 0600); err != nil {
		return "", err
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return name, err
	}
	sort.Strings(files)
	for len(files) > 10 {
		if err := os.Remove(files[0]); err != nil {
			return name, err
		}
		files = files[1:]
	}
	return name, nil
}

// Backups returns names only, preventing callers from selecting arbitrary files.
func (m *Manager) Backups() ([]string, error) {
	files, err := filepath.Glob(filepath.Join(root, "backup", "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	for i := range files {
		files[i] = filepath.Base(files[i])
	}
	return files, nil
}

func (m *Manager) RestoreBackup(name string) error {
	if name == "" || filepath.Base(name) != name || !strings.HasSuffix(name, ".json") {
		return errors.New("无效的备份名称")
	}
	file := filepath.Join(root, "backup", name)
	info, err := os.Lstat(file)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("备份必须是普通文件")
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var c config.Config
	if err := json.Unmarshal(b, &c); err != nil {
		return fmt.Errorf("备份格式无效: %w", err)
	}
	return m.Apply(c)
}

func (m *Manager) Apply(c config.Config) error {
	return m.apply(c, nil)
}

func (m *Manager) apply(c config.Config, changes []fileChange) error {
	if err := c.Normalize(); err != nil {
		return err
	}
	newJSON, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	newJSON = append(newJSON, '\n')
	oldJSON, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}
	oldNginx, err := os.ReadFile(nginxFile)
	if err != nil {
		return err
	}
	s := snapshot{Config: oldJSON, Nginx: oldNginx}
	for _, change := range changes {
		f := savedFile{Path: change.Path, Mode: change.Mode}
		f.Data, err = os.ReadFile(change.Path)
		if err == nil {
			f.Exists = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		s.Files = append(s.Files, f)
	}
	journal, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := atomicWrite(journalFile, journal, 0600); err != nil {
		return err
	}
	commit := func() error {
		for _, change := range changes {
			if change.Delete {
				if err := os.Remove(change.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			} else if err := atomicWrite(change.Path, change.Data, change.Mode); err != nil {
				return err
			}
		}
		text, err := m.Validate(c)
		if err != nil {
			return err
		}
		if _, err := m.Backup(); err != nil {
			return err
		}
		if err := atomicWrite(nginxFile, []byte(text), 0644); err != nil {
			return err
		}
		if err := atomicWrite(configFile, newJSON, 0600); err != nil {
			return err
		}
		return command("systemctl", "reload", "nginx")
	}
	if err := commit(); err != nil {
		if restoreErr := m.restore(s); restoreErr != nil {
			return fmt.Errorf("应用失败: %v；恢复失败，事务日志已保留: %w", err, restoreErr)
		}
		if removeErr := os.Remove(journalFile); removeErr != nil {
			return fmt.Errorf("应用失败: %v；已恢复，但无法清理事务日志: %w", err, removeErr)
		}
		return fmt.Errorf("应用失败，已恢复旧配置: %w", err)
	}
	if err := os.Remove(journalFile); err != nil {
		return fmt.Errorf("配置已应用但事务日志清理失败: %w", err)
	}
	return nil
}

func (m *Manager) Reload() error {
	if err := command("nginx", "-t", "-c", nginxFile); err != nil {
		return err
	}
	return command("systemctl", "reload", "nginx")
}
