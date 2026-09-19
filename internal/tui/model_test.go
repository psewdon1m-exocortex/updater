package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"updater/internal/console"
)

func loaded() Model {
	demo := NewDemo()
	m := New(demo, context.Background(), true, true)
	next, _ := m.Update(snapshotMsg{snapshot: demo.snapshot})
	return next.(Model)
}
func send(m Model, msg tea.Msg) Model     { next, _ := m.Update(msg); return next.(Model) }
func key(m Model, kind tea.KeyType) Model { return send(m, tea.KeyMsg{Type: kind}) }

func TestArrowsAndNoVimBindings(t *testing.T) {
	m := loaded()
	for _, letter := range "hjkl" {
		m = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{letter}})
	}
	if m.selected != 0 {
		t.Fatal("letter keys navigated")
	}
	m = key(m, tea.KeyDown)
	m = key(m, tea.KeyEnter)
	if m.selected != 1 || m.screen != details {
		t.Fatal("arrows did not open Neptune")
	}
	m = key(m, tea.KeyEsc)
	if m.screen != services {
		t.Fatal("Escape did not return")
	}
}

func TestCredentialFormPasteResizeAndCancel(t *testing.T) {
	m := loaded()
	m.selected = 2
	m.choice = "connect-bot"
	next, _ := m.selectHead(m.snapshot.Heads[1])
	m = next.(Model)
	m.fields[0].input.SetValue("private-bot")
	m.cursor = 1
	m.fields[0].input.Blur()
	m.fields[1].input.Focus()
	secret := "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcd"
	m = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(secret), Paste: true})
	if m.fields[1].input.Value() != secret || m.screen != form {
		t.Fatal("paste was not treated as input")
	}
	for _, size := range [][2]int{{80, 24}, {40, 16}, {100, 32}, {24, 8}} {
		m = send(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		if strings.Contains(m.View(), secret) {
			t.Fatal("credential rendered in terminal")
		}
		if size[0] >= 40 && !strings.Contains(m.View(), "****") {
			t.Fatalf("masked input is not visible: %s", m.View())
		}
		if m.fields[1].input.Value() != secret {
			t.Fatal("resize lost credential input")
		}
	}
	m = key(m, tea.KeyEsc)
	if len(m.fields) != 0 || m.pending.BotToken != "" {
		t.Fatal("cancel retained credential fields")
	}
}

func TestConfirmDefaultsToCancelAndPasteCannotSubmit(t *testing.T) {
	m := loaded()
	m.selected = 2
	m.screen = confirm
	m.pending = console.Action{Component: "gryphon", Kind: "install", HeadID: "saturn"}
	m = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("\n\rconfirm"), Paste: true})
	if m.cursor != 0 || m.screen != confirm {
		t.Fatal("paste navigated confirmation")
	}
	m = key(m, tea.KeyEnter)
	if m.screen != details || m.working {
		t.Fatal("default confirmation mutated the host")
	}
	m.screen = confirm
	m.pending = console.Action{Component: "gryphon", Kind: "install", HeadID: "saturn"}
	m = key(m, tea.KeyDown)
	next, command := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if command == nil || !m.working || m.waitingRequest == "" {
		t.Fatal("explicit confirmation did not submit")
	}
	if m.pending.BotToken != "" || len(m.fields) != 0 {
		t.Fatal("submission retained form")
	}
	next, duplicate := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if duplicate != nil {
		t.Fatal("Enter duplicated an in-flight operation")
	}
	m = send(next.(Model), command())
	if m.activeJob == nil || !m.activeJob.Finished {
		t.Fatal("did not show durable job receipt")
	}
}

func TestReconnectRecoversUncertainRequest(t *testing.T) {
	m := loaded()
	m.screen = result
	m.waitingRequest = "tui-uncertain-request-123"
	m = send(m, snapshotMsg{err: errors.New("socket disconnected")})
	if m.connected || len(m.snapshot.Jobs) == 0 {
		t.Fatal("lost last observation")
	}
	job := console.Job{ID: "durable-job", RequestID: m.waitingRequest, Component: "neptune", State: "COMPLETED", Summary: "Verified", Finished: true, UpdatedAt: time.Now()}
	snapshot := m.snapshot
	snapshot.Jobs = []console.Job{job}
	m = send(m, snapshotMsg{snapshot: snapshot})
	if !m.connected || m.activeJob == nil || m.activeJob.ID != job.ID || m.waitingRequest != "" {
		t.Fatal("reconnect did not match server request id")
	}
}

func TestAllScreensStayInsideTerminalAndExposeFocusedAction(t *testing.T) {
	for _, size := range [][2]int{{24, 8}, {32, 12}, {40, 16}, {80, 24}, {120, 40}, {200, 100}} {
		for _, page := range []screen{services, details, heads, form, confirm, result, jobs, help, bots} {
			m := loaded()
			m.width = size[0]
			m.height = size[1]
			m.selected = 1
			m.screen = page
			m.headChoices = m.snapshot.Heads
			m.pending = console.Action{Component: "neptune", Kind: "enroll", HeadID: "kernel"}
			m.fields = []field{newField("Export URL", strings.Repeat("long-url-", 20), false, 512), newField("Setup code", "synthetic-secret", true, 32)}
			m.resultLines = []string{strings.Repeat("long response ", 100)}
			m.botList = []console.Bot{{Alias: strings.Repeat("bot", 70)}}
			if page == details {
				m.cursor = len(m.menu()) - 1
			}
			if page == form {
				m.cursor = len(m.fields)
			}
			view := m.View()
			lines := strings.Split(view, "\n")
			if len(lines) > size[1] {
				t.Fatalf("page %v: %d lines exceed %d", page, len(lines), size[1])
			}
			for _, line := range lines {
				if ansi.StringWidth(line) >= size[0] {
					t.Fatalf("page %v: line wraps at %d columns: %q", page, size[0], line)
				}
			}
			if page == details && !strings.Contains(view, "Back to") {
				t.Fatalf("focused action is invisible in %v: %s", size, view)
			}
			if strings.Contains(view, "synthetic-secret") {
				t.Fatal("secret leaked")
			}
		}
	}
}

func TestUntrustedLabelsCannotSendTerminalControl(t *testing.T) {
	m := loaded()
	m.snapshot.Host = "host\x1b]52;c;c2VjcmV0\a"
	m.snapshot.Components[0].Detail = "before\x1b[2Jafter\u202e"
	view := m.View()
	if strings.ContainsAny(view, "\x1b\a\u202e") || strings.Contains(view, "c2VjcmV0") {
		t.Fatal("terminal control reached renderer")
	}
}

func TestOfflineMutationsAreDisabled(t *testing.T) {
	m := loaded()
	m.connected = false
	m.screen = details
	for _, action := range []string{"check", "install", "enroll", "connect-bot", "bots"} {
		next, cmd := m.choose(action)
		if cmd != nil || next.(Model).screen != details {
			t.Fatalf("offline action %s was dispatched", action)
		}
	}
}
