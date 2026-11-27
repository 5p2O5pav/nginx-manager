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
	"nginx-manager/internal/config"
	"nginx-manager/internal/manager"
	"nginx-manager/internal/nginx"
)

type menuModel struct {
	screen         string
	choices        []string
	selected       int
	input          bool
	value          string
	fields         []string
	form           string
	message        string
	height         int
	nginxOnline    bool
	selectedDomain string
	selectedPath   string
}

type resultMsg struct {
	text    string
	askCert string
}

func loadDomains() ([]config.Domain, error) {
	m, err := manager.Open()
	if err != nil {
		return nil, err
	}
	defer m.Close()
	c, err := m.Read()
	return c.Domains, err
}

func (m menuModel) Init() tea.Cmd {
	return func() tea.Msg {
		err := exec.Command("systemctl", "is-active", "--quiet", "nginx").Run()
		return nginxStateMsg(err == nil)
	}
}

type nginxStateMsg bool

func (m *menuModel) setScreen(screen string) {
	m.screen, m.selected, m.input, m.value, m.fields, m.form = screen, 0, false, "", nil, ""
	m.message = ""
	var domains []config.Domain
	if screen != "main" && screen != "config" {
		if ds, err := loadDomains(); err == nil {
			domains = ds
		} else {
			m.message = err.Error()
		}
	}
	switch screen {
	case "main":
		m.choices = []string{"域名管理", "转发规则管理", "证书管理", "配置管理", "退出"}
	case "domains":
		m.choices = []string{"添加域名"}
		for _, d := range domains {
			m.choices = append(m.choices, d.Name+"  |  "+enabledText(d.Enabled))
		}
		m.choices = append(m.choices, "返回")
	case "domain-actions":
		m.choices = []string{"启用/禁用", "修改域名", "删除域名", "查看证书状态", "返回域名管理"}
	case "routes":
		m.choices = []string{}
		for _, d := range domains {
			m.choices = append(m.choices, d.Name+"  |  添加规则")
			for _, r := range d.Routes {
				m.choices = append(m.choices, d.Name+"  |  "+r.Path+"  →  "+r.Target)
			}
		}
		m.choices = append(m.choices, "返回")
	case "route-list":
		m.choices = []string{}
		for _, d := range domains {
			if d.Name == m.selectedDomain {
				for _, r := range d.Routes {
					m.choices = append(m.choices, r.Path+"  →  "+r.Target)
				}
			}
		}
		m.choices = append(m.choices, "添加规则", "返回域名列表")
	case "route-actions":
		m.choices = []string{"编辑规则", "删除规则", "添加规则", "返回规则列表"}
	case "certs":
		m.choices = []string{"签发证书", "删除证书", "证书状态", "返回主菜单"}
	case "cert-domains":
		m.choices = []string{}
		for _, d := range domains {
			m.choices = append(m.choices, d.Name)
		}
		m.choices = append(m.choices, "返回证书管理")
	case "config":
		m.choices = []string{"检查配置", "应用配置（手动兜底）", "备份配置", "列出备份", "恢复备份", "上传和超时设置", "查看 Nginx 错误日志", "返回主菜单"}
	}
}

func enabledText(enabled bool) string {
	if enabled {
		return "启用"
	}
	return "禁用"
}
func domainNameChoice(choice string) string { return strings.TrimSpace(strings.Split(choice, "|")[0]) }

func (m menuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = v.Height
	case nginxStateMsg:
		m.nginxOnline = bool(v)
	case resultMsg:
		if v.askCert == "" && (m.screen == "domains" || m.screen == "routes" || m.screen == "route-list") {
			screen := m.screen
			m.setScreen(screen)
		}
		m.message = v.text
		if v.askCert != "" {
			m.input = true
			m.form = "after-add-cert"
			m.selectedDomain = v.askCert
			m.value = ""
		}
	case tea.KeyMsg:
		if m.input {
			return m.updateInput(v)
		}
		switch v.String() {
		case "ctrl+c", "q":
			if m.screen == "main" {
				return m, tea.Quit
			}
			m.setScreen("main")
		case "esc":
			if m.screen == "main" {
				return m, tea.Quit
			}
			m.back()
		case "up", "k":
			if m.selected > 0 {
				m.selected--
			}
		case "down", "j":
			if m.selected < len(m.choices)-1 {
				m.selected++
			}
		case "enter":
			return m.activate()
		}
	}
	return m, nil
}

func (m menuModel) updateInput(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEsc:
		m.input = false
		m.value = ""
		m.fields = nil
		m.message = "已取消"
	case tea.KeyBackspace, tea.KeyDelete:
		if len(m.value) > 0 {
			r := []rune(m.value)
			m.value = string(r[:len(r)-1])
		}
	case tea.KeyRunes:
		m.value += string(key.Runes)
	case tea.KeyEnter:
		v := m.value
		m.value = ""
		switch m.form {
		case "add-domain":
			m.input = false
			return m, func() tea.Msg {
				if err := run([]string{"add", v}); err != nil {
					return resultMsg{text: "添加域名失败: " + err.Error()}
				}
				name, e := config.DomainName(v)
				if e != nil {
					return resultMsg{text: e.Error()}
				}
				return resultMsg{text: "域名已添加。签发证书前请先完成对应 DNS 解析。按 Enter 默认签发，输入 n 跳过。", askCert: name}
			}
		case "after-add-cert":
			m.input = false
			if strings.EqualFold(strings.TrimSpace(v), "n") || strings.EqualFold(strings.TrimSpace(v), "no") {
				m.message = "已跳过证书签发，可稍后在证书管理中签发"
				return m, nil
			}
			name := m.selectedDomain
			return m, func() tea.Msg {
				if err := manager.Issue(name); err != nil {
					return resultMsg{text: "证书签发失败: " + err.Error()}
				}
				return resultMsg{text: "证书已签发并部署: " + name}
			}
		case "cert-issue-confirm":
			m.input = false
			name := m.fields[0]
			m.fields = nil
			return m, func() tea.Msg {
				if e := manager.Issue(name); e != nil {
					return resultMsg{text: "证书签发失败: " + e.Error()}
				}
				return resultMsg{text: "证书已签发并部署: " + name}
			}
		case "route-delete-confirm":
			vals := append([]string(nil), m.fields...)
			m.fields = nil
			m.input = false
			if !strings.EqualFold(strings.TrimSpace(v), "yes") {
				m.message = "已取消"
				return m, nil
			}
			return m, func() tea.Msg {
				if e := run([]string{"route", "delete", vals[0], vals[1]}); e != nil {
					return resultMsg{text: e.Error()}
				}
				return resultMsg{text: "规则已删除"}
			}
		case "toggle-domain", "delete-domain-confirm", "delete-cert-confirm", "restore-confirm":
			values := append(m.fields, v)
			form := m.form
			if len(values) < 2 {
				m.fields = values
				return m, nil
			}
			m.input = false
			m.fields = nil
			if !strings.EqualFold(strings.TrimSpace(values[1]), "yes") {
				m.message = "已取消"
				return m, nil
			}
			return m, func() tea.Msg {
				switch form {
				case "toggle-domain":
					ds, e := loadDomains()
					if e != nil {
						return resultMsg{text: e.Error()}
					}
					enabled := false
					for _, d := range ds {
						if d.Name == values[0] {
							enabled = !d.Enabled
						}
					}
					if e = manager.SetEnabled(values[0], enabled); e != nil {
						return resultMsg{text: e.Error()}
					}
					return resultMsg{text: "域名状态已更新"}
				case "delete-domain-confirm":
					if e := manager.DeleteDomain(values[0]); e != nil {
						return resultMsg{text: e.Error()}
					}
					return resultMsg{text: "域名已删除"}
				case "delete-cert-confirm":
					if e := manager.DeleteCertificate(values[0]); e != nil {
						return resultMsg{text: e.Error()}
					}
					return resultMsg{text: "证书已删除"}
				default:
					mgr, e := manager.Open()
					if e != nil {
						return resultMsg{text: e.Error()}
					}
					defer mgr.Close()
					if e = mgr.RestoreBackup(values[0]); e != nil {
						return resultMsg{text: e.Error()}
					}
					return resultMsg{text: "备份已恢复并应用"}
				}
			}
		case "rename-domain":
			m.fields = append(m.fields, v)
			if len(m.fields) < 3 {
				m.message = "修改域名将删除旧证书及续签记录，输入 yes 确认"
				return m, nil
			}
			vals := append([]string(nil), m.fields...)
			m.fields = nil
			m.input = false
			if !strings.EqualFold(strings.TrimSpace(vals[2]), "yes") {
				m.message = "已取消"
				return m, nil
			}
			return m, func() tea.Msg {
				if e := manager.RenameDomain(vals[0], vals[1]); e != nil {
					return resultMsg{text: e.Error()}
				}
				return resultMsg{text: "域名已修改；请重新签发证书"}
			}
		case "route-add", "route-edit":
			m.fields = append(m.fields, v)
			if len(m.fields) < 5 {
				return m, nil
			}
			vals := append([]string(nil), m.fields...)
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(vals[2])), "https://") && len(vals) < 8 {
				m.fields = vals
				return m, nil
			}
			form := m.form
			m.fields = nil
			m.input = false
			return m, func() tea.Msg {
				op := "add"
				if form == "route-edit" {
					op = "edit"
				}
				args := []string{"route", op, vals[0], vals[1], vals[2]}
				if strings.EqualFold(vals[3], "y") {
					args = append(args, "--strip")
				}
				if strings.EqualFold(vals[4], "y") {
					args = append(args, "--websocket")
				}
				if len(vals) == 8 {
					if vals[5] != "" {
						args = append(args, "--server-name="+vals[5])
					}
					if vals[6] != "" {
						args = append(args, "--ca="+vals[6])
					}
					if strings.EqualFold(vals[7], "y") {
						args = append(args, "--insecure")
					}
				}
				if e := run(args); e != nil {
					return resultMsg{text: e.Error()}
				}
				return resultMsg{text: "规则已保存并生效"}
			}
		case "settings":
			m.fields = append(m.fields, v)
			if len(m.fields) < 2 {
				return m, nil
			}
			vals := append([]string(nil), m.fields...)
			m.fields = nil
			m.input = false
			return m, func() tea.Msg {
				if e := run([]string{"settings", vals[0], vals[1]}); e != nil {
					return resultMsg{text: e.Error()}
				}
				return resultMsg{text: "设置已生效"}
			}
		case "restore-name":
			m.fields = []string{v}
			m.form = "restore-confirm"
			m.message = "输入 yes 确认恢复此备份"
		}
	}
	return m, nil
}

func (m *menuModel) startInput(form, prompt string) {
	m.input = true
	m.form = form
	m.value = ""
	m.message = prompt
}
func (m menuModel) activate() (tea.Model, tea.Cmd) {
	choice := ""
	if len(m.choices) > 0 && m.selected < len(m.choices) {
		choice = m.choices[m.selected]
	}
	switch m.screen {
	case "main":
		switch m.selected {
		case 0:
			m.setScreen("domains")
		case 1:
			m.setScreen("routes")
		case 2:
			m.setScreen("certs")
		case 3:
			m.setScreen("config")
		default:
			return m, tea.Quit
		}
	case "domains":
		if choice == "添加域名" {
			m.startInput("add-domain", "输入域名")
			return m, nil
		}
		if strings.HasPrefix(choice, "返回") {
			m.setScreen("main")
			return m, nil
		}
		m.selectedDomain = domainNameChoice(choice)
		m.setScreen("domain-actions")
	case "domain-actions":
		switch m.selected {
		case 0:
			m.startInput("toggle-domain", "输入 yes 确认切换启用状态")
			m.fields = []string{m.selectedDomain}
		case 1:
			m.form = "rename-domain"
			m.input = true
			m.fields = []string{m.selectedDomain}
			m.message = "输入新域名"
		case 2:
			m.startInput("delete-domain-confirm", "输入 yes 确认删除域名及证书")
			m.fields = []string{m.selectedDomain}
		case 3:
			name := m.selectedDomain
			return m, func() tea.Msg {
				leaf, e := nginx.CertificateInfo("/etc/nginx/ssl", name, time.Now())
				if e != nil {
					return resultMsg{text: name + ": 不可用 (" + e.Error() + ")"}
				}
				return resultMsg{text: fmt.Sprintf("%s: 有效至 %s", name, leaf.NotAfter.Format(time.RFC3339))}
			}
		default:
			m.setScreen("domains")
		}
	case "routes":
		if strings.HasPrefix(choice, "返回") {
			m.setScreen("main")
		} else {
			m.selectedDomain = domainNameChoice(choice)
			parts := strings.SplitN(choice, "|", 2)
			if len(parts) == 2 && strings.TrimSpace(parts[1]) == "添加规则" {
				m.fields = []string{m.selectedDomain}
				m.startInput("route-add", "输入匹配路径，留空为 /")
			} else if len(parts) == 2 {
				m.selectedPath = strings.TrimSpace(strings.SplitN(parts[1], "→", 2)[0])
				m.setScreen("route-actions")
			}
		}
	case "route-list":
		if choice == "返回域名列表" {
			m.setScreen("routes")
		} else if choice == "添加规则" {
			m.fields = []string{m.selectedDomain}
			m.startInput("route-add", "输入匹配路径，留空为 /")
		} else {
			m.selectedPath = strings.TrimSpace(strings.Split(choice, "→")[0])
			m.setScreen("route-actions")
		}
	case "route-actions":
		switch m.selected {
		case 0:
			m.fields = []string{m.selectedDomain, m.selectedPath}
			m.startInput("route-edit", "输入新后端地址")
		case 1:
			m.fields = []string{m.selectedDomain, m.selectedPath}
			m.startInput("route-delete-confirm", "输入 yes 确认删除此规则")
		case 2:
			m.fields = []string{m.selectedDomain}
			m.startInput("route-add", "输入匹配路径，留空为 /")
		default:
			m.setScreen("route-list")
		}
	case "certs":
		switch m.selected {
		case 0, 1:
			op := map[bool]string{true: "issue", false: "delete"}[m.selected == 0]
			m.setScreen("cert-domains")
			m.fields = []string{op}
		case 2:
			return m, func() tea.Msg {
				ds, e := loadDomains()
				if e != nil {
					return resultMsg{text: e.Error()}
				}
				var b strings.Builder
				for _, d := range ds {
					leaf, e := nginx.CertificateInfo("/etc/nginx/ssl", d.Name, time.Now())
					if e != nil {
						fmt.Fprintf(&b, "%s: 不可用 (%v)\n", d.Name, e)
					} else {
						fmt.Fprintf(&b, "%s: 有效至 %s\n", d.Name, leaf.NotAfter.Format(time.RFC3339))
					}
				}
				if b.Len() == 0 {
					return resultMsg{text: "暂无域名"}
				}
				return resultMsg{text: b.String()}
			}
		default:
			m.setScreen("main")
		}
	case "cert-domains":
		if strings.HasPrefix(choice, "返回") {
			m.setScreen("certs")
		} else {
			name := choice
			op := m.fields[0]
			if op == "issue" {
				m.message = "请确认 DNS 已解析至本机；按 Enter 开始签发"
				m.startInput("cert-issue-confirm", m.message)
				m.fields = []string{name}
			} else {
				m.startInput("delete-cert-confirm", "输入 yes 确认删除证书")
				m.fields = []string{name}
			}
		}
	case "config":
		switch m.selected {
		case 0, 1, 2:
			cmd := []string{"validate", "apply", "backup"}[m.selected]
			return m, func() tea.Msg {
				if e := run([]string{cmd}); e != nil {
					return resultMsg{text: e.Error()}
				}
				return resultMsg{text: "操作成功：" + cmd}
			}
		case 3:
			return m, func() tea.Msg {
				mgr, e := manager.Open()
				if e != nil {
					return resultMsg{text: e.Error()}
				}
				defer mgr.Close()
				fs, e := mgr.Backups()
				if e != nil {
					return resultMsg{text: e.Error()}
				}
				if len(fs) == 0 {
					return resultMsg{text: "暂无备份"}
				}
				return resultMsg{text: strings.Join(fs, "\n")}
			}
		case 4:
			m.startInput("restore-name", "输入备份文件名")
		case 5:
			m.startInput("settings", "输入设置名：max-upload-mb 或 timeout-seconds")
		case 6:
			return m, func() tea.Msg {
				f, e := os.Open("/var/log/nginx/error.log")
				if e != nil {
					return resultMsg{text: e.Error()}
				}
				defer f.Close()
				st, e := f.Stat()
				if e != nil {
					return resultMsg{text: e.Error()}
				}
				if st.Size() > 8192 {
					if _, e = f.Seek(-8192, io.SeekEnd); e != nil {
						return resultMsg{text: e.Error()}
					}
				}
				b, e := io.ReadAll(io.LimitReader(f, 8192))
				if e != nil {
					return resultMsg{text: e.Error()}
				}
				if len(b) == 0 {
					return resultMsg{text: "日志为空"}
				}
				return resultMsg{text: string(b)}
			}
		default:
			m.setScreen("main")
		}
	}
	return m, nil
}

func (m *menuModel) back() {
	switch m.screen {
	case "main":
		m.screen = "main"
	case "domain-actions":
		m.setScreen("domains")
	case "domains", "routes", "certs", "config":
		m.setScreen("main")
	case "route-list":
		m.setScreen("routes")
	case "route-actions":
		m.setScreen("route-list")
	case "cert-domains":
		m.setScreen("certs")
	default:
		m.setScreen("main")
	}
}

func (m menuModel) View() string {
	var b strings.Builder
	b.WriteString("Nginx Manager\n")
	if m.nginxOnline {
		b.WriteString("Nginx：在线\n\n")
	} else {
		b.WriteString("Nginx：离线\n\n")
	}
	if m.selectedDomain != "" && (m.screen == "domain-actions" || m.screen == "route-list" || m.screen == "route-actions") {
		fmt.Fprintf(&b, "域名：%s\n\n", m.selectedDomain)
	}
	start, end := 0, len(m.choices)
	if m.height > 0 {
		rows := m.height - 10
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
		prefix := "  "
		if i == m.selected {
			prefix = "> "
		}
		fmt.Fprintf(&b, "%s%s\n", prefix, m.choices[i])
	}
	if m.input {
		prompt := inputPrompt(m)
		fmt.Fprintf(&b, "\n%s: %s_\n", prompt, m.value)
	}
	if m.message != "" {
		msg := m.message
		if m.height > 0 {
			max := m.height - (end - start) - 8
			if max < 1 {
				max = 1
			}
			lines := strings.Split(msg, "\n")
			if len(lines) > max {
				msg = strings.Join(lines[:max], "\n") + "\n…（截断）"
			}
		}
		fmt.Fprintf(&b, "\n%s\n", msg)
	}
	b.WriteString("\n↑/↓ 选择  Enter 确认  Esc 返回  Q 退出\n")
	return b.String()
}

func inputPrompt(m menuModel) string {
	switch m.form {
	case "add-domain":
		return "域名"
	case "after-add-cert":
		return "是否签发证书？回车签发，输入 n 跳过"
	case "toggle-domain", "delete-domain-confirm", "delete-cert-confirm", "route-delete-confirm", "restore-confirm":
		return "输入 yes 确认"
	case "rename-domain":
		if len(m.fields) == 2 {
			return "输入 yes 确认"
		}
		return "新域名"
	case "route-add", "route-edit":
		prompts := []string{"路径（默认 /）", "后端地址（默认 HTTP）", "剥离路径前缀？[y/N]", "启用 WebSocket？[y/N]", "上游 TLS 名称（可空）", "CA 文件绝对路径（可空）", "关闭证书验证？[y/N]"}
		return prompts[len(m.fields)-1]
	case "restore-name":
		return "备份文件名"
	case "settings":
		if len(m.fields) == 0 {
			return "设置名称"
		}
		return "整数值"
	case "cert-issue-confirm":
		return "确认签发（回车继续，Esc 取消）"
	}
	return "输入"
}

func runMenu() error {
	m := menuModel{}
	m.setScreen("main")
	_, err := tea.NewProgram(m).Run()
	return err
}
