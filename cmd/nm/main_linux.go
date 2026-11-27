//go:build linux

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"nginx-manager/internal/config"
	"nginx-manager/internal/manager"
	"nginx-manager/internal/nginx"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return interactive()
	}
	if len(args) == 3 && args[0] == "cert" {
		switch args[1] {
		case "issue":
			fmt.Println("请确保域名 DNS 指向本机且公网 TCP 80 可访问。")
			return manager.Issue(args[2])
		case "deploy":
			return manager.Deploy(args[2])
		}
	}
	if len(args) == 4 && args[0] == "cert" && args[1] == "delete" && args[3] == "--yes" {
		return manager.DeleteCertificate(args[2])
	}
	if len(args) == 4 && args[0] == "domain" && args[1] == "delete" && args[3] == "--yes" {
		return manager.DeleteDomain(args[2])
	}
	if len(args) == 5 && args[0] == "domain" && args[1] == "rename" && args[4] == "--yes" {
		return manager.RenameDomain(args[2], args[3])
	}
	if len(args) == 3 && args[0] == "domain" && (args[1] == "enable" || args[1] == "disable") {
		return manager.SetEnabled(args[2], args[1] == "enable")
	}
	m, err := manager.Open()
	if err != nil {
		return err
	}
	defer m.Close()
	c, err := m.Read()
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			break
		}
		for _, d := range c.Domains {
			fmt.Printf("%s\t启用: %t\t规则: %d\n", d.Name, d.Enabled, len(d.Routes))
		}
		return nil
	case "add":
		if len(args) != 2 {
			break
		}
		name, err := config.DomainName(args[1])
		if err != nil {
			return err
		}
		c.Domains = append(c.Domains, config.Domain{Name: name, Enabled: true, Routes: []config.Route{}})
		if err := m.Apply(c); err != nil {
			return err
		}
		fmt.Println("域名已添加:", name)
		return nil
	case "route":
		if len(args) < 3 {
			break
		}
		name, err := config.DomainName(args[2])
		if err != nil {
			return err
		}
		index := -1
		for i := range c.Domains {
			if c.Domains[i].Name == name {
				index = i
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("域名不存在: %s", name)
		}
		d := &c.Domains[index]
		switch args[1] {
		case "list":
			if len(args) != 3 {
				break
			}
			for _, r := range d.Routes {
				fmt.Printf("%s -> %s 剥离前缀: %t WebSocket: %t\n", r.Path, r.Target, r.StripPrefix, r.WebSocket)
			}
			return nil
		case "add", "edit":
			if len(args) < 5 {
				break
			}
			r := config.Route{Path: args[3], Target: args[4]}
			for _, opt := range args[5:] {
				switch opt {
				case "--strip":
					r.StripPrefix = true
				case "--websocket":
					r.WebSocket = true
				default:
					if r.UpstreamTLS == nil {
						r.UpstreamTLS = &config.UpstreamTLS{}
					}
					switch {
					case opt == "--insecure":
						r.UpstreamTLS.InsecureSkipVerify = true
					case strings.HasPrefix(opt, "--server-name="):
						r.UpstreamTLS.ServerName = strings.TrimPrefix(opt, "--server-name=")
					case strings.HasPrefix(opt, "--ca="):
						r.UpstreamTLS.CAFile = strings.TrimPrefix(opt, "--ca=")
					default:
						return fmt.Errorf("未知选项: %s", opt)
					}
				}
			}
			r, err = config.NormalizeRoute(r)
			if err != nil {
				return err
			}
			if args[1] == "edit" {
				found := false
				for i := range d.Routes {
					if d.Routes[i].Path == r.Path {
						d.Routes[i] = r
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("规则不存在: %s", r.Path)
				}
			} else {
				d.Routes = append(d.Routes, r)
			}
			if err := m.Apply(c); err != nil {
				return err
			}
			fmt.Println("规则已保存")
			return nil
		case "delete":
			if len(args) != 4 {
				break
			}
			for i, r := range d.Routes {
				if r.Path == args[3] {
					d.Routes = append(d.Routes[:i], d.Routes[i+1:]...)
					if err := m.Apply(c); err != nil {
						return err
					}
					fmt.Println("规则已删除")
					return nil
				}
			}
			return fmt.Errorf("规则不存在: %s", args[3])
		}
		return fmt.Errorf("用法: nm route list 域名 | add 域名 路径 后端 [--strip] [--websocket] | delete 域名 路径")
	case "validate":
		if len(args) != 1 {
			break
		}
		_, err := m.Validate(c)
		if err == nil {
			fmt.Println("配置检查通过")
		}
		return err
	case "settings":
		if len(args) != 3 {
			break
		}
		value, err := strconv.Atoi(args[2])
		if err != nil {
			return fmt.Errorf("设置值必须是整数")
		}
		switch args[1] {
		case "max-upload-mb":
			c.Settings.MaxUploadMB = value
		case "timeout-seconds":
			c.Settings.ProxyTimeoutSeconds = value
		default:
			return fmt.Errorf("未知设置: %s", args[1])
		}
		if err := m.Apply(c); err != nil {
			return err
		}
		fmt.Println("设置已应用")
		return nil
	case "apply", "rebuild":
		if len(args) != 1 {
			break
		}
		if err := m.Apply(c); err != nil {
			return err
		}
		fmt.Println("配置已应用")
		return nil
	case "reload":
		if len(args) != 1 {
			break
		}
		if err := m.Reload(); err != nil {
			return err
		}
		fmt.Println("Nginx 已重载")
		return nil
	case "backup":
		if len(args) != 1 {
			break
		}
		name, err := m.Backup()
		if err != nil {
			return err
		}
		fmt.Println("备份:", name)
		return nil
	case "backups":
		if len(args) != 1 {
			break
		}
		files, err := m.Backups()
		if err != nil {
			return err
		}
		for _, f := range files {
			fmt.Println(f)
		}
		return nil
	case "restore":
		if len(args) != 3 || args[2] != "--yes" {
			break
		}
		if err := m.RestoreBackup(args[1]); err != nil {
			return err
		}
		fmt.Println("备份已恢复并应用")
		return nil
	case "cert":
		if len(args) < 2 || args[1] != "status" || len(args) > 3 {
			break
		}
		found := false
		for _, d := range c.Domains {
			if len(args) == 3 && args[2] != d.Name {
				continue
			}
			found = true
			leaf, err := nginx.CertificateInfo("/etc/nginx/ssl", d.Name, time.Now())
			if err != nil {
				fmt.Printf("%s: 不可用 (%v)\n", d.Name, err)
			} else {
				fmt.Printf("%s: 有效，有效期至 %s\n", d.Name, leaf.NotAfter.Format(time.RFC3339))
			}
		}
		if len(args) == 3 && !found {
			return fmt.Errorf("域名不存在: %s", args[2])
		}
		return nil
	}
	return fmt.Errorf("未知命令或参数；请查看 README.md 中的命令说明")
}

func interactive() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("请使用 root 运行")
	}
	if _, err := os.Stat("/etc/nm/config.json"); os.IsNotExist(err) {
		reader := bufio.NewReader(os.Stdin)
		if _, err := exec.LookPath("nginx"); err != nil {
			fmt.Print("未安装 Nginx，是否使用 apt 安装？[Y/n] ")
			s, err := reader.ReadString('\n')
			if err != nil {
				return err
			}
			if s = strings.ToLower(strings.TrimSpace(s)); s != "" && s != "y" && s != "yes" {
				return fmt.Errorf("已取消安装")
			}
			cmd := exec.Command("apt-get", "install", "-y", "nginx")
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("安装 Nginx 失败: %w", err)
			}
		}
		if _, err := os.Stat("/root/.acme.sh/acme.sh"); err != nil {
			fmt.Println("未检测到 ACME.sh：证书签发功能需要先安装 ACME.sh。")
		}
		fmt.Println("本程序将完全接管 Nginx，已有站点可能停止工作。原配置将备份至 /etc/nm/nginx-original-backup。")
		fmt.Print("是否接管 Nginx？[Y/n] ")
		s, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if answer := strings.ToLower(strings.TrimSpace(s)); answer != "" && answer != "y" && answer != "yes" {
			return fmt.Errorf("已取消接管")
		}
		if err := manager.Initialize(); err != nil {
			return err
		}
		fmt.Println("初始化完成。")
	} else if err != nil {
		return err
	}
	return runMenu()
}
