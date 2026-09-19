package tui

import (
	"context"
	"errors"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"strconv"
	"strings"
	"time"
	"updater/internal/console"
)

func wyvernManagementAction(action string) bool {
	switch action {
	case "connect-kernel", "adapter-put", "profile-put", "adapter-disable", "adapter-enable", "adapter-delete", "client-grant", "client-revoke":
		return true
	}
	return false
}
func (m Model) chooseWyvernManagement(action string) (tea.Model, tea.Cmd) {
	m.choice = action
	if action == "connect-kernel" {
		return m.openWyvernForm(0)
	}
	backend, ok := m.backend.(console.WyvernEditBackend)
	if !ok {
		m.notice = "The connected console does not support Wyvern configuration"
		return m, nil
	}
	m.working, m.screen = true, result
	m.resultLines = []string{"Reading the current configuration revision..."}
	m.candidate, m.activeJob = nil, nil
	m.generation++
	generation := m.generation
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 20*time.Second)
		defer cancel()
		state, err := backend.WyvernEdit(ctx)
		return replyMsg{generation: generation, kind: "wyvern-form", wyvernEdit: &state, err: err}
	}
}
func (m Model) openWyvernForm(revision int) (tea.Model, tea.Cmd) {
	m.clearFields()
	m.screen, m.cursor, m.offset = form, 0, 0
	m.pending = console.Action{Component: "wyvern", Kind: m.choice, Wyvern: &console.WyvernInput{Revision: revision}}
	switch m.choice {
	case "connect-kernel":
		m.fields = []field{newField("Kernel HTTPS origin", "", false, 512), newField("Kernel Access Key", "", true, 0)}
	case "adapter-put":
		m.fields = []field{newField("Adapter ID", "", false, 64), newField("Adapter name", "", false, 100), newField("Google model", "", false, 100),
			newField("API key (empty keeps existing key)", "", true, 4096), newField("Maximum output tokens", "8192", false, 5),
			newField("Capabilities (comma separated)", "text,streaming,structured_output,token_count,image,pdf,audio,video,youtube", false, 160)}
	case "profile-put":
		m.fields = []field{newField("Adapter ID", "", false, 64), newField("Profile name", "default", false, 64), newField("Google model", "", false, 100),
			newField("Maximum output tokens", "8192", false, 5), newField("Capabilities (comma separated)", "text,structured_output,token_count", false, 160)}
	case "adapter-disable", "adapter-enable", "adapter-delete":
		m.fields = []field{newField("Adapter ID", "", false, 64)}
	case "client-revoke":
		m.fields = []field{newField("Client ID", "", false, 64)}
	case "client-grant":
		m.fields = []field{newField("Client ID", "", false, 64), newField("Allowed Adapter IDs (comma separated; empty revokes all)", "", false, 1040)}
	}
	m.fields[0].input.Focus()
	return m, textinput.Blink
}
func splitWyvernList(raw string) []string {
	result := []string{}
	for _, part := range strings.Split(raw, ",") {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, value)
		}
	}
	return result
}
func (m *Model) readWyvernForm() error {
	i := m.pending.Wyvern
	if i == nil {
		return errors.New("Wyvern form is unavailable")
	}
	v := func(index int) string { return m.fields[index].input.Value() }
	switch m.choice {
	case "connect-kernel":
		i.KernelURL, i.AccessKey = v(0), v(1)
	case "adapter-put", "profile-put":
		i.AdapterID, i.Model = v(0), v(2)
		maxIndex, capIndex := 3, 4
		if m.choice == "adapter-put" {
			i.Name, i.APIKey = v(1), v(3)
			maxIndex, capIndex = 4, 5
		} else {
			i.Profile = v(1)
		}
		maximum, err := strconv.Atoi(v(maxIndex))
		if err != nil {
			return errors.New("Maximum output tokens must be a whole number")
		}
		i.MaxOutput, i.Capabilities = maximum, splitWyvernList(v(capIndex))
	case "adapter-disable", "adapter-enable", "adapter-delete":
		i.AdapterID = v(0)
	case "client-revoke":
		i.ClientID = v(0)
	case "client-grant":
		i.ClientID, i.AllowedAdapters = v(0), splitWyvernList(v(1))
	}
	return console.ValidateAction(m.pending)
}
func (d *Demo) WyvernEdit(context.Context) (console.WyvernEditState, error) {
	return console.WyvernEditState{Revision: 1}, nil
}
