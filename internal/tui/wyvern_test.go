package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"testing"
)

func TestWyvernCatalogShowsUnboundClientAndControlsUseHostScope(t *testing.T) {
	m := loaded()
	m.selected = 3
	m.screen = details
	next, command := m.choose("adapters")
	if command == nil {
		t.Fatal("Adapter diagnostics did not dispatch")
	}
	m = send(next.(Model), command())
	lines := strings.Join(m.resultLines, "\n")
	for _, expected := range []string{"Google", "mastermind", "laboratory", "Adapter not selected", "crusher -> google"} {
		if !strings.Contains(lines, expected) {
			t.Fatalf("missing %s: %s", expected, lines)
		}
	}
	m.screen = details
	next, _ = m.choose("drain")
	m = next.(Model)
	if m.screen != confirm || m.pending.HeadID != "" || m.pending.Component != "wyvern" || m.cursor != 0 {
		t.Fatal("Wyvern control must be host-scoped and default to cancel")
	}
	m.width, m.height = 100, 32
	if !strings.Contains(m.View(), "every client") {
		t.Fatal("shared impact is not visible")
	}
	m = key(m, tea.KeyDown)
	next, command = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if command == nil {
		t.Fatal("confirmed operation not submitted")
	}
	m = send(next.(Model), command())
	if m.activeJob == nil || !m.activeJob.Finished || m.activeJob.Component != "wyvern" {
		t.Fatal("no durable operation receipt")
	}
}

func TestWyvernLifecycleActionsAndOfflineBoundary(t *testing.T) {
	m := loaded()
	m.selected = 3
	m.screen = details
	available := map[string]bool{}
	for _, item := range m.menu() {
		available[item.action] = true
	}
	for _, action := range []string{"install", "check", "repair", "connect-kernel", "adapter-put"} {
		if !available[action] {
			t.Fatalf("missing %s", action)
		}
	}
	m.connected = false
	for _, action := range []string{"adapters", "drain", "resume", "reload", "install", "check", "repair", "adapter-put"} {
		next, command := m.choose(action)
		if command != nil || next.(Model).screen != details {
			t.Fatal("offline operation dispatched")
		}
	}
}

func TestWyvernSecretsStayMaskedThroughConfirmAndCancel(t *testing.T) {
	for _, action := range []string{"connect-kernel", "adapter-put"} {
		m := loaded()
		m.selected = 3
		m.choice = action
		m.width = 100
		m.height = 32
		revision := 7
		if action == "connect-kernel" {
			revision = 0
		}
		next, _ := m.openWyvernForm(revision)
		m = next.(Model)
		index := 1
		if action == "adapter-put" {
			index = 3
			m.fields[0].input.SetValue("google")
			m.fields[1].input.SetValue("Google")
			m.fields[2].input.SetValue("test-model")
		} else {
			m.fields[0].input.SetValue("https://kernel.example.test")
		}
		secret := "synthetic-secret-canary"
		m.fields[index].input.SetValue(secret)
		if strings.Contains(m.View(), secret) {
			t.Fatal("secret rendered in form")
		}
		if err := m.readWyvernForm(); err != nil {
			t.Fatal(err)
		}
		m.screen = confirm
		if strings.Contains(m.View(), secret) {
			t.Fatal("secret rendered in confirmation")
		}
		m = key(m, tea.KeyEsc)
		if m.pending.Wyvern != nil || len(m.fields) != 0 {
			t.Fatal("cancel retained credentials")
		}
	}
}
