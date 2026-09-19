package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

var componentNames = []string{"updater", "neptune", "gryphon", "wyvern"}

type pollMsg struct{}
type snapshotMsg struct {
	snapshot console.Snapshot
	err      error
}
type replyMsg struct {
	generation int
	candidate  *console.Candidate
	job        *console.Job
	bots       []console.Bot
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
	noColor, demo               bool
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
	generation                  int
	waitingRequest              string
}

func New(backend console.Backend, ctx context.Context, noColor, demo bool) Model {
	return Model{backend: backend, ctx: ctx, width: 80, height: 24, noColor: noColor, demo: demo, screen: services, polling: true}
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
	items := []menuItem{{"Refresh status", "refresh"}}
	if m.selected < 3 {
		items = append(items, menuItem{"Check for updates", "check"})
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
	return append(items, menuItem{"Help / diagnostics", "help"}, menuItem{"Back to applications", "back"})
}

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
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
		return m, nil
	case tea.KeyMsg:
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
			if m.screen == services {
				return m, tea.Quit
			}
			m.clearFields()
			m.pending = console.Action{}
			previous := m.screen
			m.screen = details
			if previous == details {
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
			if key == "up" {
				m.selected = max(0, m.selected-1)
			}
			if key == "down" {
				m.selected = min(3, m.selected+1)
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
			if key == "left" {
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
			if key == "enter" && !m.working && m.candidate != nil && m.candidate.UpdateAvailable && m.connected {
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
			if field.input.Value() == "" {
				m.notice = "Complete both fields before continuing."
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
	if m.working || !m.connected {
		m.notice = "Wait for the operator connection before submitting."
		return m, nil
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
		ctx, cancel := context.WithTimeout(m.ctx, 48*time.Second)
		defer cancel()
		job, err := m.backend.Act(ctx, action)
		action.BotToken, action.SetupCode = "", ""
		return replyMsg{generation: generation, kind: "action", job: &job, err: err}
	}
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
		{ID: "wyvern", Process: "planned", Health: "not integrated", Detail: "Wyvern integration is planned."},
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
			item.Installed = true
			item.Process = "active"
			item.Health = "ready"
			item.Detail = "DEMO: local API is responding."
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

var _ console.Backend = (*Demo)(nil)
