//go:build linux

package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAfterAddPromptsBeforeIssue(t *testing.T) {
	m := menuModel{screen: "domains"}
	model, cmd := m.Update(resultMsg{text: "已添加", askCert: "example.com"})
	m = model.(menuModel)
	if !m.input || m.selectedDomain != "example.com" || !strings.Contains(m.View(), "回车签发") {
		t.Fatal("未显示签发确认")
	}
	if cmd != nil {
		t.Fatal("未确认时不应签发")
	}
	m.value = "n"
	model, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || model.(menuModel).input {
		t.Fatal("跳过不应执行签发")
	}
}

func TestRouteFormAndDeleteConfirmation(t *testing.T) {
	m := menuModel{screen: "route-protocol", choices: []string{"HTTP（未加密）", "HTTPS（加密）"}, selected: 0, selectedDomain: "example.com"}
	model, cmd := m.activate()
	m = model.(menuModel)
	if cmd != nil || !m.input || m.form != "route-add" || m.fields[1] != "http" {
		t.Fatal("默认应选择 HTTP 并开始输入地址")
	}
	steps := []string{"", "3000", "", "", ""}
	for i, v := range steps {
		if !strings.Contains(m.View(), []string{"后端地址", "端口", "路径", "剥离", "WebSocket"}[i]) {
			t.Fatalf("第 %d 步提示错误: %s", i+1, m.View())
		}
		m.value = v
		model, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = model.(menuModel)
		if i < len(steps)-1 && cmd != nil {
			t.Fatal("输入尚未完成，不应提交")
		}
	}
	if cmd == nil {
		t.Fatal("全部输入完成后应提交规则")
	}
	if got := composeTarget("http", "127.0.0.1", "3000"); got != "http://127.0.0.1:3000" {
		t.Fatalf("got target %q", got)
	}
	m = menuModel{screen: "route-actions", choices: []string{"删除规则", "返回规则列表"}, fields: []string{"example.com", "/api/"}}
	model, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || model.(menuModel).screen != "route-delete-confirm" || len(model.(menuModel).choices) != 2 {
		t.Fatal("应先显示两项删除确认菜单")
	}
	model, cmd = model.(menuModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("第二次回车应执行删除")
	}
}

func TestComposeIPv6Target(t *testing.T) {
	if got := composeTarget("https", "[::1]", "8443"); got != "https://[::1]:8443" {
		t.Fatalf("got %q", got)
	}
}

func TestStripPrefixDefaultsOff(t *testing.T) {
	if affirmative("") || affirmative("n") || affirmative("no") {
		t.Fatal("空输入或否定答案不应剥离前缀")
	}
	if !affirmative("y") || !affirmative(" YES ") {
		t.Fatal("y/yes 应启用剥离前缀")
	}
}

func TestBackendAddressDefaultsToLoopbackHTTP(t *testing.T) {
	if got := backendOrDefault(""); got != "http://127.0.0.1" {
		t.Fatalf("got %q", got)
	}
	if got := backendOrDefault(" 127.0.0.1:3000 "); got != "127.0.0.1:3000" {
		t.Fatalf("got %q", got)
	}
}
