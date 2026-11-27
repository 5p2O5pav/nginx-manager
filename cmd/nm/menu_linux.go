//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"nginx-manager/internal/manager"
	"nginx-manager/internal/nginx"
)

type menuModel struct {
	choices  []string
	selected int
	input    bool
	value    string
	message  string
	form     string
	fields   []string
	height   int
}

type resultMsg string

func (m menuModel) Init() tea.Cmd { return nil }
func (m menuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = v.Height
	case resultMsg:
		m.message = string(v)
	case tea.KeyMsg:
		if m.input {
			switch v.Type {
			case tea.KeyEsc:
				m.input = false
				m.value = ""
				m.fields = nil
			case tea.KeyBackspace, tea.KeyDelete:
				if len(m.value) > 0 {
					r := []rune(m.value)
					m.value = string(r[:len(r)-1])
				}
			case tea.KeyEnter:
				name := m.value
				m.value = ""
				if m.form == "domain-enable" || m.form == "domain-disable" {
					enabled := m.form == "domain-enable"
					m.input = false
					return m, func() tea.Msg {
						if err := manager.SetEnabled(name, enabled); err != nil {
							return resultMsg("错误: " + err.Error())
						}
						if enabled {
							return resultMsg("域名已启用，请检查证书状态")
						}
						return resultMsg("域名已禁用")
					}
				}
				if m.form == "domain-rename" {
					m.fields = append(m.fields, name)
					if len(m.fields) < 3 {
						return m, nil
					}
					values := append([]string(nil), m.fields...)
					m.fields = nil
					m.input = false
					if !strings.EqualFold(strings.TrimSpace(values[2]), "yes") {
						m.message = "已取消"
						return m, nil
					}
					return m, func() tea.Msg {
						if err := manager.RenameDomain(values[0], values[1]); err != nil {
							return resultMsg("错误: " + err.Error())
						}
						return resultMsg("域名已修改，新域名需要重新签发证书")
					}
				}
				if m.form == "settings" {
					m.fields = append(m.fields, name)
					if len(m.fields) < 2 {
						return m, nil
					}
					values := append([]string(nil), m.fields...)
					m.fields = nil
					m.input = false
					return m, func() tea.Msg {
						if err := run([]string{"settings", values[0], values[1]}); err != nil {
							return resultMsg("错误: " + err.Error())
						}
						return resultMsg("设置已应用")
					}
				}
				if m.form == "domain-delete" || m.form == "restore" {
					form := m.form
					m.fields = append(m.fields, name)
					if len(m.fields) < 2 {
						return m, nil
					}
					values := append([]string(nil), m.fields...)
					m.fields = nil
					m.input = false
					if !strings.EqualFold(strings.TrimSpace(values[1]), "yes") {
						m.message = "已取消"
						return m, nil
					}
					return m, func() tea.Msg {
						if form == "domain-delete" {
							if err := manager.DeleteDomain(values[0]); err != nil {
								return resultMsg("错误: " + err.Error())
							}
							return resultMsg("域名及其证书已删除")
						}
						mgr, err := manager.Open()
						if err != nil {
							return resultMsg("错误: " + err.Error())
						}
						defer mgr.Close()
						if err := mgr.RestoreBackup(values[0]); err != nil {
							return resultMsg("错误: " + err.Error())
						}
						return resultMsg("备份已恢复并应用")
					}
				}
				if m.form == "cert-delete" {
					m.fields = append(m.fields, name)
					if len(m.fields) < 2 {
						return m, nil
					}
					values := append([]string(nil), m.fields...)
					m.fields = nil
					m.input = false
					if !strings.EqualFold(strings.TrimSpace(values[1]), "yes") {
						m.message = "已取消删除"
						return m, nil
					}
					return m, func() tea.Msg {
						if err := manager.DeleteCertificate(values[0]); err != nil {
							return resultMsg("错误: " + err.Error())
						}
						return resultMsg("证书已删除，HTTPS 已关闭")
					}
				}
				if m.form == "route-list" {
					m.input = false
					return m, func() tea.Msg {
						mgr, err := manager.Open()
						if err != nil {
							return resultMsg("错误: " + err.Error())
						}
						defer mgr.Close()
						c, err := mgr.Read()
						if err != nil {
							return resultMsg("错误: " + err.Error())
						}
						for _, d := range c.Domains {
							if d.Name == strings.ToLower(strings.TrimSpace(name)) {
								if len(d.Routes) == 0 {
									return resultMsg("该域名没有转发规则")
								}
								var b strings.Builder
								for _, r := range d.Routes {
									fmt.Fprintf(&b, "%s → %s  剥离: %t  WebSocket: %t\n", r.Path, r.Target, r.StripPrefix, r.WebSocket)
								}
								return resultMsg(b.String())
							}
						}
						return resultMsg("域名不存在: " + name)
					}
				}
				if m.form == "route-delete" {
					m.fields = append(m.fields, name)
					if len(m.fields) < 3 {
						return m, nil
					}
					values := append([]string(nil), m.fields...)
					m.fields = nil
					m.input = false
					if !strings.EqualFold(strings.TrimSpace(values[2]), "yes") {
						m.message = "已取消删除"
						return m, nil
					}
					return m, func() tea.Msg {
						if err := run([]string{"route", "delete", values[0], values[1]}); err != nil {
							return resultMsg("错误: " + err.Error())
						}
						return resultMsg("规则已删除")
					}
				}
				if m.form == "cert" {
					m.input = false
					return m, func() tea.Msg {
						if err := manager.Issue(name); err != nil {
							return resultMsg("签发失败: " + err.Error())
						}
						return resultMsg("证书已签发并部署: " + name)
					}
				}
				if m.form == "route" || m.form == "route-edit" {
					m.fields = append(m.fields, name)
					if len(m.fields) < 5 {
						return m, nil
					}
					if strings.HasPrefix(strings.ToLower(strings.TrimSpace(m.fields[2])), "https://") && len(m.fields) < 8 {
						return m, nil
					}
					values := append([]string(nil), m.fields...)
					form := m.form
					m.fields = nil
					m.input = false
					return m, func() tea.Msg {
						operation := "add"
						if form == "route-edit" {
							operation = "edit"
						}
						args := []string{"route", operation, values[0], values[1], values[2]}
						if strings.EqualFold(values[3], "y") || strings.EqualFold(values[3], "yes") {
							args = append(args, "--strip")
						}
						if strings.EqualFold(values[4], "y") || strings.EqualFold(values[4], "yes") {
							args = append(args, "--websocket")
						}
						if len(values) == 8 {
							if strings.TrimSpace(values[5]) != "" {
								args = append(args, "--server-name="+strings.TrimSpace(values[5]))
							}
							if strings.TrimSpace(values[6]) != "" {
								args = append(args, "--ca="+strings.TrimSpace(values[6]))
							}
							if strings.EqualFold(strings.TrimSpace(values[7]), "y") || strings.EqualFold(strings.TrimSpace(values[7]), "yes") {
								args = append(args, "--insecure")
							}
						}
						if err := run(args); err != nil {
							return resultMsg("错误: " + err.Error())
						}
						return resultMsg("规则添加成功")
					}
				}
				m.input = false
				return m, func() tea.Msg {
					if err := run([]string{"add", name}); err != nil {
						return resultMsg("错误: " + err.Error())
					}
					return resultMsg("域名添加成功: " + name)
				}
			case tea.KeyRunes:
				m.value += string(v.Runes)
			}
			return m, nil
		}
		switch v.String() {
		case "ctrl+c", "q", "esc":
			return m, tea.Quit
		case "up", "k":
			if m.selected > 0 {
				m.selected--
			}
		case "down", "j":
			if m.selected < len(m.choices)-1 {
				m.selected++
			}
		case "enter":
			switch m.selected {
			case 0:
				return m, func() tea.Msg {
					mgr, err := manager.Open()
					if err != nil {
						return resultMsg("错误: " + err.Error())
					}
					defer mgr.Close()
					c, err := mgr.Read()
					if err != nil {
						return resultMsg("错误: " + err.Error())
					}
					if len(c.Domains) == 0 {
						return resultMsg("暂无域名")
					}
					var b strings.Builder
					for _, d := range c.Domains {
						fmt.Fprintf(&b, "%s  启用: %t  规则: %d\n", d.Name, d.Enabled, len(d.Routes))
					}
					return resultMsg(b.String())
				}
			case 1:
				m.input = true
				m.form = "domain"
				m.message = ""
			case 2:
				m.input = true
				m.form = "route"
				m.fields = nil
				m.message = ""
			case 3:
				m.input = true
				m.form = "route-list"
				m.message = ""
			case 4:
				m.input = true
				m.form = "route-delete"
				m.fields = nil
				m.message = ""
			case 5:
				m.input = true
				m.form = "cert"
				m.message = "请确保域名 DNS 指向本机且公网 TCP 80 可访问。"
			case 6:
				m.input = true
				m.form = "cert-delete"
				m.fields = nil
				m.message = "将取消自动续签、删除证书并关闭该域名的 HTTPS。"
			case 7, 8, 9:
				commands := map[int]string{7: "validate", 8: "apply", 9: "backup"}
				cmd := commands[m.selected]
				return m, func() tea.Msg {
					if err := run([]string{cmd}); err != nil {
						return resultMsg("错误: " + err.Error())
					}
					return resultMsg("操作完成: " + cmd)
				}
			case 10:
				m.input = true
				m.form = "domain-delete"
				m.fields = nil
				m.message = "将删除域名配置、证书及续签记录。"
			case 11:
				m.input = true
				m.form = "route-edit"
				m.fields = nil
				m.message = "按已有路径查找并替换规则。"
			case 12:
				return m, func() tea.Msg {
					mgr, err := manager.Open()
					if err != nil {
						return resultMsg("错误: " + err.Error())
					}
					defer mgr.Close()
					files, err := mgr.Backups()
					if err != nil {
						return resultMsg("错误: " + err.Error())
					}
					if len(files) == 0 {
						return resultMsg("暂无备份")
					}
					return resultMsg(strings.Join(files, "\n"))
				}
			case 13:
				m.input = true
				m.form = "restore"
				m.fields = nil
				m.message = "输入备份列表中的文件名，恢复将覆盖当前配置。"
			case 14:
				return m, func() tea.Msg {
					out, err := exec.Command("systemctl", "status", "nginx", "--no-pager", "--lines=10").CombinedOutput()
					if err != nil {
						return resultMsg(fmt.Sprintf("Nginx 状态: %v\n%s", err, out))
					}
					return resultMsg(out)
				}
			case 15:
				return m, func() tea.Msg {
					f, err := os.Open("/var/log/nginx/error.log")
					if err != nil {
						return resultMsg("错误: " + err.Error())
					}
					defer f.Close()
					st, err := f.Stat()
					if err != nil {
						return resultMsg("错误: " + err.Error())
					}
					if st.Size() > 8192 {
						if _, err := f.Seek(-8192, io.SeekEnd); err != nil {
							return resultMsg("错误: " + err.Error())
						}
					}
					b, err := io.ReadAll(io.LimitReader(f, 8192))
					if err != nil {
						return resultMsg("错误: " + err.Error())
					}
					if len(b) == 0 {
						return resultMsg("日志为空")
					}
					return resultMsg(b)
				}
			case 16:
				return m, func() tea.Msg {
					mgr, err := manager.Open()
					if err != nil {
						return resultMsg("错误: " + err.Error())
					}
					defer mgr.Close()
					c, err := mgr.Read()
					if err != nil {
						return resultMsg("错误: " + err.Error())
					}
					if len(c.Domains) == 0 {
						return resultMsg("暂无域名")
					}
					var b strings.Builder
					for _, d := range c.Domains {
						leaf, err := nginx.CertificateInfo("/etc/nginx/ssl", d.Name, time.Now())
						if err != nil {
							fmt.Fprintf(&b, "%s: 不可用 (%v)\n", d.Name, err)
						} else {
							fmt.Fprintf(&b, "%s: 有效，有效期至 %s\n", d.Name, leaf.NotAfter.Format(time.RFC3339))
						}
					}
					return resultMsg(b.String())
				}
			case 17:
				m.input = true
				m.form = "settings"
				m.fields = nil
				m.message = "可设置 max-upload-mb（0 表示不限）或 timeout-seconds（0 使用默认值）。"
			case 18:
				m.input = true
				m.form = "domain-enable"
				m.message = ""
			case 19:
				m.input = true
				m.form = "domain-disable"
				m.message = ""
			case 20:
				m.input = true
				m.form = "domain-rename"
				m.fields = nil
				m.message = "修改域名会删除旧证书及续签记录，新域名需要重新签发。"
			case 21:
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m menuModel) View() string {
	var b strings.Builder
	b.WriteString("Nginx Manager\n\n")
	start, end := 0, len(m.choices)
	if m.height > 0 {
		rows := m.height - 9
		if rows < 3 {
			rows = 3
		}
		if end > rows {
			if m.selected >= rows {
				start = m.selected - rows + 1
			}
			end = start + rows
			if end > len(m.choices) {
				end = len(m.choices)
			}
		}
	}
	for i := start; i < end; i++ {
		choice := m.choices[i]
		cursor := "  "
		if i == m.selected {
			cursor = "> "
		}
		fmt.Fprintf(&b, "%s%s\n", cursor, choice)
	}
	if m.input {
		prompt := "输入域名"
		if m.form == "route" || m.form == "route-edit" {
			prompt = []string{"输入域名", "匹配路径（默认 /）", "后端地址（默认 HTTP）", "剥离前缀？[y/N]", "启用 WebSocket？[y/N]", "上游 TLS 名称（默认后端主机）", "CA 文件绝对路径（默认系统 CA）", "关闭上游证书验证？[y/N]"}[len(m.fields)]
		}
		if m.form == "route-delete" {
			prompt = []string{"输入域名", "要删除的规则路径", "输入 yes 确认删除"}[len(m.fields)]
		}
		if m.form == "cert-delete" {
			prompt = []string{"输入域名", "输入 yes 确认删除证书"}[len(m.fields)]
		}
		if m.form == "domain-delete" {
			prompt = []string{"输入域名", "输入 yes 确认删除域名与证书"}[len(m.fields)]
		}
		if m.form == "restore" {
			prompt = []string{"备份文件名", "输入 yes 确认恢复"}[len(m.fields)]
		}
		if m.form == "settings" {
			prompt = []string{"设置名称", "整数值"}[len(m.fields)]
		}
		if m.form == "domain-rename" {
			prompt = []string{"原域名", "新域名", "输入 yes 确认"}[len(m.fields)]
		}
		fmt.Fprintf(&b, "\n%s: %s_\n", prompt, m.value)
	}
	if m.message != "" {
		message := m.message
		if m.height > 0 {
			lines := strings.Split(message, "\n")
			limit := m.height - (end - start) - 6
			if limit < 1 {
				limit = 1
			}
			if len(lines) > limit {
				message = strings.Join(lines[:limit], "\n") + "\n…（内容已截断）"
			}
		}
		fmt.Fprintf(&b, "\n%s\n", message)
	}
	b.WriteString("\n方向键选择，Enter 确认，Esc/Q 退出\n")
	return b.String()
}

func runMenu() error {
	_, err := tea.NewProgram(menuModel{choices: []string{"域名列表", "添加域名", "添加转发规则", "查看转发规则", "删除转发规则", "签发证书", "删除证书", "检查配置", "应用配置", "备份配置", "删除域名", "编辑转发规则", "列出备份", "恢复备份", "Nginx 状态", "Nginx 错误日志", "证书状态", "上传和超时设置", "启用域名", "禁用域名", "修改域名", "退出"}}).Run()
	return err
}
