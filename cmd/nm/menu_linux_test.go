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
	m := menuModel{input: true, form: "route-add", fields: []string{"example.com"}}
	if !strings.Contains(m.View(), "路径") {
		t.Fatal("应先输入路径")
	}
	m.value = "/api/"
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(menuModel)
	if cmd != nil || !strings.Contains(m.View(), "后端地址") {
		t.Fatal("应继续输入后端")
	}
	m = menuModel{input: true, form: "route-delete-confirm", fields: []string{"example.com", "/api/"}, value: "no"}
	model, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || model.(menuModel).message != "已取消" {
		t.Fatal("未确认时不应删除")
	}
}
