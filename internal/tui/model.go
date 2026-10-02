package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"updater/internal/console"
)

type screen int

const (
	services screen = iota
	details
	heads
	form
	confirm
	result
	jobs
	help
	bots
)

var componentNames = []string{"updater", "neptune", "gryphon", "wyvern", "window"}

func windowComponentIndex() int {
	for index, name := range componentNames {
		if name == "window" {
			return index
		}
	}
	panic("Window operator component is missing")
}

type pollMsg struct{}
type observedMsg struct{ err error }
type snapshotMsg struct {
	snapshot console.Snapshot
	err      error
}
type replyMsg struct {
	generation int
	candidate  *console.Candidate
	job        *console.Job
	bots       []console.Bot
	lines      []string
	wyvernEdit *console.WyvernEditState
	imagePlan  *console.ImagePlan
	kind       string
	err        error
}
type field struct {
	label  string
	input  textinput.Model
	secret bool
}
type menuItem struct{ label, action string }

type Model struct {
	backend                     console.Backend
	ctx                         context.Context
	width, height               int
	noColor, demo, windowOnly   bool
	screen                      screen
	selected, cursor, offset    int
	snapshot                    console.Snapshot
	connected, polling, working bool
	errorText, notice           string
	choice                      string
	headChoices                 []console.Head
	fields                      []field
	pending                     console.Action
	candidate                   *console.Candidate
	activeJob                   *console.Job
	botList                     []console.Bot
	resultLines                 []string
	imagePlan                   *console.ImagePlan
	generation                  int
	waitingRequest              string
}

func New(backend console.Backend, ctx context.Context, noColor, demo bool) Model {
	return Model{backend: backend, ctx: ctx, width: 80, height: 24, noColor: noColor, demo: demo, screen: services, polling: true}
}

func NewWindowOnly(backend console.Backend, ctx context.Context, noColor bool) Model {
	m := New(backend, ctx, noColor, false)
	m.windowOnly = true
	m.selected = windowComponentIndex()
	m.screen = details
	return m
}

func (m Model) Init() tea.Cmd { return tea.Batch(m.refresh(), tick()) }
func tick() tea.Cmd           { return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return pollMsg{} }) }
func (m Model) refresh() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 8*time.Second)
		defer cancel()
		snapshot, err := m.backend.Snapshot(ctx)
		if err != nil && !m.demo {
			snapshot.Components = console.LocalComponents(ctx, "")
		}
		return snapshotMsg{snapshot, err}
	}
}

func (m Model) component() console.Component {
	for _, item := range m.snapshot.Components {
		if item.ID == componentNames[m.selected] {
			return item
		}
	}
	return console.Component{ID: componentNames[m.selected], Process: "unknown", Health: "waiting", Detail: "Waiting for a status observation."}
}

func (m Model) menu() []menuItem {
	items := []menuItem{{"Refresh status", "refresh"}, {"Set fallback release repository", "set-source"}}
	if m.component().ID == "window" {
		items = append(items, menuItem{"Check for releases", "check"})
		if m.component().Installed {
			items = append(items, menuItem{"Pair development PC public key", "pair"}, menuItem{"Open read-only grant", "open"}, menuItem{"Revoke access now", "revoke"}, menuItem{"Start observed operator shell", "observe"}, menuItem{"Repair Window runtime", "repair"})
		} else {
			items = append(items, menuItem{"Install Window on this host", "install"})
		}
		items = append(items, menuItem{"Operation history", "jobs"})
		items = append(items, menuItem{"Help / diagnostics", "help"})
		if !m.windowOnly {
			items = append(items, menuItem{"Back to applications", "back"})
		}
		return items
	}
	if m.selected == 0 {
		items = append(items, menuItem{"Set host Kernel machine connection", "set-kernel"}, menuItem{"Review Docker image storage", "images"})
	}
	if m.selected < 3 {
		if m.component().Installed {
			items = append(items, menuItem{"Check for updates", "check"})
		}
		if m.selected > 0 && !m.component().Installed {
			items = append(items, menuItem{"Install " + title(m.component().ID), "install"})
		}
		if m.selected == 1 {
			items = append(items, menuItem{"Initialize / link project", "enroll"})
		}
		if m.selected == 2 {
			items = append(items, menuItem{"List registered bots", "bots"}, menuItem{"Connect a bot", "connect-bot"})
		}
		items = append(items, menuItem{"Operation history", "jobs"})
	}
	if m.component().ID == "wyvern" {
		items = append(items, menuItem{"Adapters and client bindings", "adapters"})
		if m.component().Installed {
			items = append(items, menuItem{"Reload configuration", "reload"}, menuItem{"Pause new requests (drain)", "drain"}, menuItem{"Resume requests", "resume"})
		}
		items = append(items, menuItem{"Operation history", "jobs"})
		items = append(items, menuItem{"Connect Kernel", "connect-kernel"}, menuItem{"Create / edit Google Adapter", "adapter-put"},
			menuItem{"Configure Adapter profile", "profile-put"}, menuItem{"Disable Adapter", "adapter-disable"}, menuItem{"Enable Adapter", "adapter-enable"},
			menuItem{"Delete unused Adapter", "adapter-delete"}, menuItem{"Grant Adapters to client", "client-grant"}, menuItem{"Revoke client", "client-revoke"})
		if m.component().Installed {
			items = append(items, menuItem{"Link registered service", "install"})
		} else {
			items = append(items, menuItem{"Install Wyvern on this host", "install"})
		}
		if m.component().Installed {
			items = append(items, menuItem{"Check for updates", "check"})
		}
		items = append(items, menuItem{"Repair runtime", "repair"})
	}
	return append(items, menuItem{"Help / diagnostics", "help"}, menuItem{"Back to applications", "back"})
}

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case observedMsg:
		m.screen = details
		m.notice = "Observed shell closed."
		if msg.err != nil {
			m.notice = "Observed shell: " + console.Text(msg.err.Error())
		}
		return m, m.refresh()
	case tea.WindowSizeMsg:
		m.width = max(1, msg.Width)
		m.height = max(1, msg.Height)
		for i := range m.fields {
			m.fields[i].input.Width = max(1, min(100, m.width-9))
			m.fields[i].input.SetCursor(m.fields[i].input.Position())
		}
		return m, nil
	case pollMsg:
		if m.polling {
			return m, tick()
		}
		m.polling = true
		return m, tea.Batch(m.refresh(), tick())
	case snapshotMsg:
		m.polling = false
		m.connected = msg.err == nil
		if msg.err != nil {
			m.errorText = console.Text(msg.err.Error())
			if len(msg.snapshot.Components) > 0 {
				m.snapshot.Components = msg.snapshot.Components
			}
		} else {
			m.snapshot = msg.snapshot
			m.errorText = ""
			for _, job := range msg.snapshot.Jobs {
				if m.activeJob != nil && m.activeJob.ID == job.ID || m.waitingRequest != "" && m.waitingRequest == job.RequestID {
					copy := job
					if m.activeJob != nil && m.activeJob.ID == job.ID {
						copy.PairCommand = m.activeJob.PairCommand
						copy.PairExpiresAt = m.activeJob.PairExpiresAt
						copy.PairBotUsername = m.activeJob.PairBotUsername
					}
					m.activeJob = &copy
					m.waitingRequest = ""
				}
			}
		}
		if m.screen == details {
			m.cursor = min(m.cursor, len(m.menu())-1)
		}
		return m, nil
	case replyMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		m.working = false
		if m.screen != result {
			m.notice = "Request finished. Open operation history, or repeat the release check to review its result."
			if msg.job != nil && msg.err == nil {
				m.activeJob = msg.job
				m.waitingRequest = ""
			}
			return m, nil
		}
		m.cursor = 0
		m.offset = 0
		if msg.err != nil {
			if msg.kind == "image-clean" || msg.kind == "images" {
				m.imagePlan = nil
			}
			m.resultLines = []string{console.Text(msg.err.Error())}
			if msg.kind == "action" {
				m.resultLines = append(m.resultLines, "Check operation history before retrying.", "Request: "+m.waitingRequest)
			}
			m.screen = result
			return m, nil
		}
		if msg.candidate != nil {
			m.candidate = msg.candidate
			m.screen = result
			m.resultLines = nil
		}
		if msg.job != nil {
			m.activeJob = msg.job
			m.screen = result
			m.resultLines = nil
			m.waitingRequest = ""
			if !m.polling {
				m.polling = true
				return m, m.refresh()
			}
		}
		if msg.kind == "bots" {
			m.botList = msg.bots
			m.screen = bots
		}
		if msg.kind == "adapters" {
			m.resultLines = msg.lines
		}
		if msg.imagePlan != nil {
			m.imagePlan = msg.imagePlan
			m.resultLines = imagePlanLines(*msg.imagePlan)
		}
		if msg.kind == "image-clean" {
			m.imagePlan = nil
			m.resultLines = msg.lines
		}
		if msg.wyvernEdit != nil {
			return m.openWyvernForm(msg.wyvernEdit.Revision)
		}
		return m, nil
	case tea.KeyMsg:
		if m.windowOnly {
			m.selected = windowComponentIndex()
		}
		// Bracketed paste is data, never navigation, confirmation or a command.
		if msg.Paste {
			if m.screen == form && m.cursor < len(m.fields) {
				var cmd tea.Cmd
				m.fields[m.cursor].input, cmd = m.fields[m.cursor].input.Update(msg)
				return m, cmd
			}
			return m, nil
		}
		key := msg.String()
		if key == "ctrl+c" {
			m.clearFields()
			return m, tea.Quit
		}
		if key == "esc" {
			if m.screen == services || m.windowOnly && m.screen == details {
				return m, tea.Quit
			}
			m.clearFields()
			m.pending = console.Action{}
			m.imagePlan = nil
			previous := m.screen
			m.screen = details
			if previous == details && !m.windowOnly {
				m.screen = services
			}
			m.cursor = 0
			m.offset = 0
			// Only an accepted daemon job survives navigation. Ignore stale UI replies.
			if !m.working {
				m.generation++
			}
			return m, nil
		}
		if m.screen == form {
			return m.updateForm(msg)
		}
		switch m.screen {
		case services:
			if m.windowOnly {
				m.screen = details
				return m, nil
			}
			if key == "up" {
				m.selected = max(0, m.selected-1)
			}
			if key == "down" {
				m.selected = min(len(componentNames)-1, m.selected+1)
			}
			if key == "enter" || key == "right" {
				m.screen = details
				m.cursor = 0
				m.offset = 0
			}
		case details:
			items := m.menu()
			if key == "up" {
				m.cursor = max(0, m.cursor-1)
			}
			if key == "down" {
				m.cursor = min(len(items)-1, m.cursor+1)
			}
			if key == "left" && !m.windowOnly {
				m.screen = services
				m.cursor = 0
				m.offset = 0
			}
			if key == "enter" {
				return m.choose(items[min(m.cursor, len(items)-1)].action)
			}
		case heads:
			if key == "up" {
				m.cursor = max(0, m.cursor-1)
			}
			if key == "down" {
				m.cursor = min(len(m.headChoices)-1, m.cursor+1)
			}
			if key == "enter" && len(m.headChoices) > 0 {
				return m.selectHead(m.headChoices[m.cursor])
			}
		case confirm:
			if key == "up" || key == "left" {
				m.cursor = 0
			}
			if key == "down" || key == "right" {
				m.cursor = 1
			}
			if key == "tab" {
				m.cursor = 1 - m.cursor
			}
			if key == "enter" {
				if m.cursor == 0 {
					m.clearFields()
					m.pending = console.Action{}
					m.screen = details
					return m, nil
				}
				return m.submit()
			}
		case result:
			if key == "enter" && !m.windowOnly && !m.working && m.imagePlan != nil && len(m.imagePlan.Candidates) > 0 && m.connected {
				m.pending = console.Action{Component: "updater", Kind: "image-clean"}
				m.screen, m.cursor, m.offset = confirm, 0, 0
			} else if key == "enter" && !m.working && m.candidate != nil && m.candidate.UpdateAvailable && m.connected && (!m.windowOnly || m.candidate.Component == "window") {
				m.pending = console.Action{Component: m.candidate.Component, Kind: "update", HeadID: m.candidate.HeadID, Version: m.candidate.Available}
				m.screen = confirm
				m.cursor = 0
				m.offset = 0
			} else if key == "enter" && !m.working {
				m.screen = details
				m.cursor = 0
				m.offset = 0
			}
			if key == "up" {
				m.offset = max(0, m.offset-1)
			}
			if key == "down" {
				m.offset++
			}
		case jobs:
			list := m.visibleJobs()
			if key == "up" {
				m.cursor = max(0, m.cursor-1)
			}
			if key == "down" {
				m.cursor = min(max(0, len(list)-1), m.cursor+1)
			}
			if key == "enter" && len(list) > 0 {
				copy := list[m.cursor]
				m.activeJob = &copy
				m.candidate = nil
				m.resultLines = nil
				m.screen = result
				m.offset = 0
			}
		case help, bots:
			if key == "up" {
				m.offset = max(0, m.offset-1)
			}
			if key == "down" {
				m.offset++
			}
			if key == "enter" {
				m.screen = details
				m.cursor = 0
				m.offset = 0
			}
		}
	}
	return m, nil
}

func (m Model) choose(action string) (tea.Model, tea.Cmd) {
	if m.windowOnly {
		m.selected = windowComponentIndex()
		switch action {
		case "refresh", "set-source", "check", "pair", "open", "revoke", "observe", "repair", "install", "jobs", "help":
		default:
			m.notice = "This operator session is limited to Window."
			return m, nil
		}
	}
	m.notice = ""
	m.offset = 0
	switch action {
	case "back":
		m.screen = services
		m.cursor = 0
		return m, nil
	case "help":
		m.screen = help
		return m, nil
	case "jobs":
		m.screen = jobs
		m.cursor = 0
		return m, nil
	case "refresh":
		if !m.polling {
			m.polling = true
			return m, m.refresh()
		}
		return m, nil
	}
	if !m.connected {
		m.notice = "Operator API unavailable. Refresh status or open diagnostics."
		return m, nil
	}
	if m.working {
		m.notice = "A request is still pending. Open operation history to inspect accepted work."
		return m, nil
	}
	if action == "images" && m.component().ID == "updater" {
		backend, ok := m.backend.(console.ImageBackend)
		if !ok {
			m.notice = "Docker image diagnostics are unavailable in this console"
			return m, nil
		}
		m.generation++
		generation := m.generation
		m.working, m.screen = true, result
		m.imagePlan, m.candidate, m.activeJob = nil, nil, nil
		m.resultLines = []string{"Checking Docker images and rollback protection..."}
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(m.ctx, 22*time.Second)
			defer cancel()
			plan, err := backend.ImagePlan(ctx)
			return replyMsg{generation: generation, kind: "images", imagePlan: &plan, err: err}
		}
	}
	if m.component().ID == "window" {
		switch action {
		case "observe":
			return m, tea.ExecProcess(exec.Command("/usr/local/bin/window", "observe"), func(err error) tea.Msg { return observedMsg{err: err} })
		case "pair", "open":
			m.choice = action
			m.pending = console.Action{Component: "window", Kind: action}
			m.clearFields()
			if action == "pair" {
				m.fields = []field{newField("Development PC ssh-ed25519 public key", "", false, 512)}
			}
			if action == "open" {
				m.fields = []field{newField("Duration in minutes (1–120)", "20", false, 3)}
			}
			m.fields[0].input.Focus()
			m.screen, m.cursor = form, 0
			return m, textinput.Blink
		case "revoke", "repair":
			m.pending = console.Action{Component: "window", Kind: action}
			m.screen, m.cursor = confirm, 0
			return m, nil
		}
	}
	if action == "set-source" {
		m.choice = action
		m.pending = console.Action{Component: m.component().ID, Kind: action}
		m.clearFields()
		m.fields = []field{newField("Fallback HTTPS repository URL", m.component().FallbackURL, false, 512)}
		m.fields[0].input.Focus()
		m.screen, m.cursor = form, 0
		return m, textinput.Blink
	}
	if action == "set-kernel" {
		m.choice = action
		m.pending = console.Action{Component: "updater", Kind: action}
		m.clearFields()
		m.fields = []field{newField("Kernel HTTPS origin", m.snapshot.KernelURL, false, 512), newField("Protected machine token file", m.snapshot.KernelTokenFile, false, 512), newField("Host instance ID", m.snapshot.HostID, false, 64)}
		m.fields[0].input.Focus()
		m.screen, m.cursor = form, 0
		return m, textinput.Blink
	}
	if m.component().ID == "wyvern" {
		if wyvernManagementAction(action) {
			return m.chooseWyvernManagement(action)
		}
		if action == "adapters" {
			backend, ok := m.backend.(console.WyvernBackend)
			if !ok {
				m.notice = "The connected console does not support Wyvern diagnostics"
				return m, nil
			}
			m.generation++
			generation := m.generation
			m.working, m.screen = true, result
			m.candidate, m.activeJob = nil, nil
			m.resultLines = []string{"Reading Adapters and client bindings..."}
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(m.ctx, 8*time.Second)
				defer cancel()
				view, err := backend.Wyvern(ctx)
				return replyMsg{generation: generation, kind: "adapters", lines: wyvernLines(view), err: err}
			}
		}
		if action == "reload" || action == "drain" || action == "resume" || action == "repair" {
			m.pending = console.Action{Component: "wyvern", Kind: action}
			m.screen, m.cursor = confirm, 0
			return m, nil
		}
		if action == "check" {
			m.activeJob, m.candidate = nil, nil
			m.working, m.screen = true, result
			m.resultLines = []string{"Checking the shared Wyvern release..."}
			m.generation++
			generation := m.generation
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(m.ctx, 48*time.Second)
				defer cancel()
				candidate, err := m.backend.Check(ctx, "wyvern", "")
				return replyMsg{generation: generation, kind: "check", candidate: &candidate, err: err}
			}
		}
		if action != "install" {
			return m, nil
		}
		if !m.component().Installed {
			m.pending = console.Action{Component: "wyvern", Kind: "install"}
			m.screen, m.cursor = confirm, 0
			return m, nil
		}
	}
	if action == "bots" {
		m.generation++
		generation := m.generation
		m.working = true
		m.screen = result
		m.resultLines = []string{"Reading bot registrations..."}
		m.candidate = nil
		m.activeJob = nil
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(m.ctx, 8*time.Second)
			defer cancel()
			list, err := m.backend.Bots(ctx)
			return replyMsg{generation: generation, kind: "bots", bots: list, err: err}
		}
	}
	m.choice = action
	if (m.component().ID == "gryphon" || m.component().ID == "window") && action == "check" {
		m.activeJob, m.candidate = nil, nil
		m.working, m.screen = true, result
		m.resultLines = []string{"Checking the shared " + console.Text(m.component().ID) + " release..."}
		m.generation++
		generation := m.generation
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(m.ctx, 48*time.Second)
			defer cancel()
			candidate, err := m.backend.Check(ctx, m.component().ID, "")
			return replyMsg{generation: generation, kind: "check", candidate: &candidate, err: err}
		}
	}
	if m.component().ID == "gryphon" && action == "connect-bot" {
		m.pending = console.Action{Component: "gryphon", Kind: action}
		m.clearFields()
		m.fields = []field{newField("Bot alias", "", false, 48), newField("Telegram bot token", "", true, 210)}
		m.fields[0].input.Focus()
		m.screen = form
		m.cursor = 0
		return m, textinput.Blink
	}
	if action == "check" {
		m.activeJob, m.candidate = nil, nil
		m.working, m.screen = true, result
		m.resultLines = []string{"Checking the host component release..."}
		m.generation++
		generation := m.generation
		kind := m.component().ID
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(m.ctx, 48*time.Second)
			defer cancel()
			candidate, err := m.backend.Check(ctx, kind, "")
			return replyMsg{generation: generation, kind: "check", candidate: &candidate, err: err}
		}
	}
	if action == "install" && m.component().ID != "wyvern" {
		m.pending = console.Action{Component: m.component().ID, Kind: action}
		m.screen, m.cursor = confirm, 0
		return m, nil
	}
	m.headChoices = nil
	for _, head := range m.snapshot.Heads {
		if console.Eligible(head, m.component().ID) {
			m.headChoices = append(m.headChoices, head)
		}
	}
	if len(m.headChoices) == 0 {
		m.notice = "No eligible registered service. Check Updater host registration and configuration."
		return m, nil
	}
	m.screen = heads
	m.cursor = 0
	return m, nil
}

func (m Model) selectHead(head console.Head) (tea.Model, tea.Cmd) {
	m.cursor = 0
	m.offset = 0
	m.activeJob = nil
	m.candidate = nil
	m.resultLines = nil
	m.pending = console.Action{Component: m.component().ID, Kind: m.choice, HeadID: head.ID}
	if m.choice == "check" {
		m.working = true
		m.screen = result
		m.resultLines = []string{"Checking published releases..."}
		m.generation++
		generation := m.generation
		kind := m.component().ID
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(m.ctx, 48*time.Second)
			defer cancel()
			candidate, err := m.backend.Check(ctx, kind, head.ID)
			return replyMsg{generation: generation, kind: "check", candidate: &candidate, err: err}
		}
	}
	if m.choice == "install" {
		m.screen = confirm
		return m, nil
	}
	m.clearFields()
	m.screen = form
	if m.choice == "enroll" {
		m.fields = []field{newField("Loopback backup export URL", head.ExportURL, false, 512), newField("Saturn setup code", "", true, 32)}
	}
	if m.choice == "connect-bot" {
		m.fields = []field{newField("Bot alias", "", false, 48), newField("Telegram bot token", "", true, 210)}
	}
	if len(m.fields) > 0 {
		m.fields[0].input.Focus()
	}
	return m, textinput.Blink
}

func newField(label, value string, secret bool, limit int) field {
	input := textinput.New()
	input.Prompt = ""
	input.CharLimit = limit
	input.KeyMap.Paste.SetEnabled(false)
	input.SetValue(value)
	input.Width = 36
	if secret {
		input.EchoMode = textinput.EchoPassword
		input.EchoCharacter = '*'
	}
	return field{label: label, input: input, secret: secret}
}

func (m Model) updateForm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "tab", "down", "shift+tab", "up":
		delta := 1
		if key.String() == "up" || key.String() == "shift+tab" {
			delta = -1
		}
		for i := range m.fields {
			m.fields[i].input.Blur()
		}
		m.cursor = (m.cursor + delta + len(m.fields) + 1) % (len(m.fields) + 1)
		if m.cursor < len(m.fields) {
			return m, m.fields[m.cursor].input.Focus()
		}
		return m, nil
	case "enter":
		if m.cursor < len(m.fields) {
			m.fields[m.cursor].input.Blur()
			m.cursor++
			if m.cursor < len(m.fields) {
				return m, m.fields[m.cursor].input.Focus()
			}
			return m, nil
		}
		for _, field := range m.fields {
			if field.input.Value() == "" && !(m.choice == "adapter-put" && field.label == "API key (empty keeps existing key)") && !(m.choice == "client-grant" && field.label == "Allowed Adapter IDs (comma separated; empty revokes all)") && !(m.choice == "connect-kernel" && field.label == "Host instance ID (empty: derived from machine-id)") {
				m.notice = "Complete the required fields before continuing."
				return m, nil
			}
		}
		if m.choice == "enroll" {
			m.pending.ExportURL = m.fields[0].input.Value()
			m.pending.SetupCode = m.fields[1].input.Value()
			if len(m.pending.SetupCode) != 32 {
				m.notice = "The Saturn setup code must contain 32 characters."
				return m, nil
			}
		}
		if m.choice == "connect-bot" {
			m.pending.Alias = m.fields[0].input.Value()
			m.pending.BotToken = m.fields[1].input.Value()
		}
		if m.choice == "set-source" {
			m.pending.RepositoryURL = m.fields[0].input.Value()
		}
		if m.choice == "set-kernel" {
			m.pending.KernelURL, m.pending.KernelTokenFile, m.pending.HostID = m.fields[0].input.Value(), m.fields[1].input.Value(), m.fields[2].input.Value()
		}
		if m.choice == "pair" {
			m.pending.PublicKey = m.fields[0].input.Value()
		}
		if m.choice == "open" {
			minutes, err := strconv.Atoi(m.fields[0].input.Value())
			if err != nil {
				m.notice = "Enter a duration in minutes."
				return m, nil
			}
			m.pending.Minutes = minutes
		}
		if wyvernManagementAction(m.choice) {
			if err := m.readWyvernForm(); err != nil {
				m.notice = err.Error()
				return m, nil
			}
		}
		if err := console.ValidateAction(m.pending); err != nil {
			m.notice = err.Error()
			return m, nil
		}
		m.screen = confirm
		m.cursor = 0
		m.offset = 0
		m.notice = ""
		return m, nil
	}
	if m.cursor < len(m.fields) {
		var cmd tea.Cmd
		m.fields[m.cursor].input, cmd = m.fields[m.cursor].input.Update(key)
		return m, cmd
	}
	return m, nil
}

func (m *Model) clearFields() {
	for i := range m.fields {
		m.fields[i].input.SetValue("")
	}
	m.fields = nil
}
func (m Model) submit() (tea.Model, tea.Cmd) {
	if m.windowOnly && (m.pending.Component != "window" || !windowOperatorAction(m.pending.Kind)) {
		m.notice = "This operator session is limited to Window."
		return m, nil
	}
	if m.working || !m.connected {
		m.notice = "Wait for the operator connection before submitting."
		return m, nil
	}
	if m.pending.Kind == "image-clean" {
		backend, ok := m.backend.(console.ImageBackend)
		if !ok || m.imagePlan == nil {
			m.notice = "Review a fresh Docker image plan before cleanup"
			return m, nil
		}
		planID := m.imagePlan.ID
		m.pending = console.Action{}
		m.screen, m.cursor, m.offset = result, 0, 0
		m.working = true
		m.resultLines = []string{"Rechecking the reviewed image plan and removing one bounded batch..."}
		m.generation++
		generation := m.generation
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(m.ctx, 50*time.Second)
			defer cancel()
			result, err := backend.CleanImages(ctx, planID)
			return replyMsg{generation: generation, kind: "image-clean", lines: imageCleanLines(result), err: err}
		}
	}
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		m.notice = "Cannot allocate an operation ID."
		return m, nil
	}
	action := m.pending
	action.RequestID = "tui-" + hex.EncodeToString(bytes)
	m.waitingRequest = action.RequestID
	m.pending = console.Action{}
	m.clearFields()
	m.candidate = nil
	m.activeJob = nil
	m.resultLines = []string{"Submitting operation...", "Request: " + action.RequestID}
	m.screen = result
	m.cursor = 0
	m.offset = 0
	m.working = true
	m.generation++
	generation := m.generation
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 60*time.Second)
		defer cancel()
		job, err := m.backend.Act(ctx, action)
		action.BotToken, action.SetupCode = "", ""
		return replyMsg{generation: generation, kind: "action", job: &job, err: err}
	}
}

func windowOperatorAction(kind string) bool {
	switch kind {
	case "set-source", "pair", "open", "revoke", "repair", "install", "update":
		return true
	}
	return false
}

func (m Model) visibleJobs() []console.Job {
	var list []console.Job
	for _, job := range m.snapshot.Jobs {
		if job.Component == m.component().ID {
			list = append(list, job)
		}
	}
	return list
}
func title(value string) string {
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

// Demo never calls the OS/service adapters and uses synthetic values only.
type Demo struct {
	mu       sync.Mutex
	snapshot console.Snapshot
}

func NewDemo() *Demo {
	now := time.Now().UTC()
	return &Demo{snapshot: console.Snapshot{Protocol: console.Protocol, Host: "demo-host", ObservedAt: now, Components: []console.Component{
		{ID: "updater", Installed: true, Process: "active", Health: "ready", Version: "0.5.0", Detail: "Host update worker is responding."},
		{ID: "neptune", Installed: true, Process: "active", Health: "ready", Version: "0.1.8", Detail: "Backup agent is responding. Schedules are managed in Saturn."},
		{ID: "gryphon", Process: "inactive", Health: "not installed", Detail: "Install the Telegram gateway to connect a bot."},
		{ID: "wyvern", Installed: true, Process: "active", Health: "ready", Version: "0.0.1", Detail: "LLM gateway is responding. Review Adapters and client bindings."},
		{ID: "window", Installed: true, Process: "active", Health: "ready", Version: "0.0.1", Detail: "DEMO: paired development PC; read-only grant closed."},
	}, Heads: []console.Head{{ID: "kernel", Service: "kernel", Helpers: []string{"neptune"}, ExportURL: "http://127.0.0.1:18180/api/internal/neptune/backup"}, {ID: "saturn", Service: "saturn", Helpers: []string{"neptune", "gryphon"}, ExportURL: "http://127.0.0.1:3000/api/v1/internal/neptune/backup"}}, Jobs: []console.Job{{ID: "demo-previous-job", Component: "neptune", HeadID: "kernel", State: "COMPLETED", Version: "0.1.8", Summary: "Operation completed and verified", UpdatedAt: now, Finished: true}}}}
}
func (d *Demo) Snapshot(context.Context) (console.Snapshot, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := d.snapshot
	result.ObservedAt = time.Now().UTC()
	result.Components = append([]console.Component(nil), result.Components...)
	result.Jobs = append([]console.Job(nil), result.Jobs...)
	return result, nil
}
func (d *Demo) Check(_ context.Context, kind, head string) (console.Candidate, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	current := ""
	for _, item := range d.snapshot.Components {
		if item.ID == kind {
			current = item.Version
		}
	}
	available := "0.5.1"
	if kind == "neptune" {
		available = "0.1.9"
	}
	if kind == "gryphon" {
		available = "0.1.5"
	}
	if kind == "window" {
		available = "0.0.2"
	}
	return console.Candidate{Component: kind, HeadID: head, Installed: current, Available: available, UpdateAvailable: current != available}, nil
}
func (d *Demo) Act(_ context.Context, a console.Action) (console.Job, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	job := console.Job{ID: "demo-" + a.RequestID, RequestID: a.RequestID, Component: a.Component, HeadID: a.HeadID, State: "COMPLETED", Summary: "DEMO: simulated completion; no host changes", UpdatedAt: time.Now().UTC(), Finished: true}
	for _, existing := range d.snapshot.Jobs {
		if existing.RequestID == a.RequestID {
			return existing, nil
		}
	}
	for i := range d.snapshot.Components {
		item := &d.snapshot.Components[i]
		if item.ID == a.Component {
			previousHealth := item.Health
			item.Installed = true
			item.Process = "active"
			item.Health = "ready"
			item.Detail = "DEMO: local API is responding."
			if a.Component == "wyvern" {
				if a.Kind == "drain" {
					item.Health = "draining"
				}
				if a.Kind == "reload" {
					item.Health = previousHealth
				}
			}
			if a.Version != "" {
				item.Version = a.Version
			} else if item.Version == "" {
				item.Version = "0.1.4"
			}
		}
	}
	d.snapshot.Jobs = append([]console.Job{job}, d.snapshot.Jobs...)
	if len(d.snapshot.Jobs) > 100 {
		d.snapshot.Jobs = d.snapshot.Jobs[:100]
	}
	return job, nil
}
func (d *Demo) Bots(context.Context) ([]console.Bot, error) {
	return []console.Bot{{Alias: "example", Username: "example_bot", State: "ready"}}, nil
}

func (d *Demo) ImagePlan(context.Context) (console.ImagePlan, error) {
	return console.ImagePlan{ID: strings.Repeat("0", 64), ObservedAt: time.Now().UTC(), OwnedImages: 3, ProtectedImages: 3, Candidates: []console.ImageItem{}}, nil
}

func (d *Demo) CleanImages(context.Context, string) (console.ImageCleanResult, error) {
	return console.ImageCleanResult{}, nil
}

var _ console.Backend = (*Demo)(nil)
