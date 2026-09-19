package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"updater/internal/console"
)

type row struct {
	text   string
	focus  bool
	accent bool
	raw    bool
}

func (m Model) View() string {
	width := min(m.width, 160)
	height := min(m.height, 80)
	if width < 24 || height < 8 {
		return ansi.Truncate("Terminal too small. Resize (24x8 minimum); Ctrl+C exits.", max(1, width-1), "")
	}
	content := m.rows()
	var lines []row
	focus := -1
	for _, line := range content {
		value := line.text
		if !line.raw {
			value = console.Text(value)
		}
		for _, part := range strings.Split(ansi.Wrap(value, max(1, width-4), ""), "\n") {
			if line.focus && focus < 0 {
				focus = len(lines)
			}
			lines = append(lines, row{text: part, focus: line.focus, accent: line.accent, raw: line.raw})
		}
	}
	available := height - 6
	start := min(max(0, m.offset), max(0, len(lines)-available))
	if focus >= 0 {
		if focus < start {
			start = focus
		}
		if focus >= start+available {
			start = focus - available + 1
		}
	}
	connection := "CONNECTING"
	if m.connected {
		connection = "CONNECTED"
	} else if m.errorText != "" {
		connection = "OFFLINE"
	}
	host := console.Text(m.snapshot.Host)
	if host == "" {
		host = "local host"
	}
	header := " EXOCORTEX / UPDATER"
	if m.demo {
		header += "  [DEMO]"
	}
	status := " " + host + " | " + connection
	if !m.snapshot.ObservedAt.IsZero() {
		status += " | observed " + m.snapshot.ObservedAt.Local().Format("15:04:05")
	}
	if m.screen == confirm {
		header = " CONFIRM / " + title(m.pending.Component)
		if m.demo {
			header = " [DEMO]" + header
		}
		status = " " + console.Text(m.pending.HeadID) + " | " + actionLabel(m.pending.Kind)
		if m.pending.Version != "" {
			status = " " + console.Text(m.pending.HeadID) + " | update " + m.pending.Version
		}
	}
	output := []string{m.paint(ansi.Truncate(header, width-1, ""), true, false), ansi.Truncate(status, width-1, ""), strings.Repeat("-", width-1)}
	for index := 0; index < available; index++ {
		position := start + index
		if position < len(lines) {
			line := lines[position]
			prefix := "  "
			if line.focus {
				prefix = "> "
			}
			text := ansi.Truncate(prefix+line.text, width-1, "")
			if m.noColor && !line.raw {
				text = ansi.Strip(text)
			}
			output = append(output, m.paint(text, line.accent, line.focus))
		} else {
			output = append(output, "")
		}
	}
	output = append(output, strings.Repeat("-", width-1))
	foot := "Up/Down select  Enter open  Esc back"
	if m.screen == services {
		foot = "Up/Down select  Enter open  Esc exit"
	}
	if m.screen == form {
		foot = "Tab/Up/Down field  Enter next  Esc cancel"
	}
	if m.screen == confirm {
		foot = "Up/Down choose  Enter accept  Esc cancel"
	}
	if m.screen == jobs || m.screen == help || m.screen == bots {
		foot = "Up/Down scroll  Enter open/back  Esc back"
	}
	if m.screen == result {
		foot = "Up/Down scroll  Enter continue  Esc back"
	}
	if width < 50 {
		foot = "Up/Down  Enter  Esc"
		if m.screen == form {
			foot = "Tab  Enter  Esc"
		}
	}
	if len(lines) > available && width >= 65 {
		foot = fmt.Sprintf("%d-%d/%d | %s", start+1, min(start+available, len(lines)), len(lines), foot)
	}
	output = append(output, ansi.Truncate(" "+foot, width-1, ""))
	// One spare column prevents autowrap on clients with delayed-wrap behavior.
	return strings.Join(output, "\n")
}

func (m Model) paint(text string, accent, focus bool) string {
	if m.noColor {
		return text
	}
	if focus {
		return "\x1b[1;36m" + text + "\x1b[0m"
	}
	if accent {
		return "\x1b[36m" + text + "\x1b[0m"
	}
	return text
}

func (m Model) rows() []row {
	var rows []row
	add := func(text string) { rows = append(rows, row{text: text}) }
	heading := func(text string) { rows = append(rows, row{text: text, accent: true}) }
	selectRow := func(text string, selected bool) { rows = append(rows, row{text: text, focus: selected}) }
	component := m.component()
	switch m.screen {
	case services:
		heading("SERVICE APPLICATIONS")
		add("Manage the applications on this host.")
		add("")
		for index, kind := range componentNames {
			item := console.Component{ID: kind, Health: "waiting"}
			for _, current := range m.snapshot.Components {
				if current.ID == kind {
					item = current
				}
			}
			label := fmt.Sprintf("%-9s  %s", title(kind), item.Health)
			if m.width >= 65 && item.Version != "" {
				label += "  v" + item.Version
			}
			selectRow(label, index == m.selected)
		}
		add("")
		heading(title(component.ID))
		add(component.Detail)
		if m.errorText != "" {
			add("")
			add(m.errorText)
		}
	case details:
		heading(title(component.ID))
		version := component.Version
		if version == "" {
			version = "unknown"
		}
		add("Process: " + component.Process + " | API: " + component.Health)
		add("Running version: " + version)
		add(component.Detail)
		add("")
		if m.notice != "" {
			add(m.notice)
			add("")
		}
		for index, item := range m.menu() {
			selectRow(item.label, index == m.cursor)
		}
	case heads:
		heading(title(component.ID) + " / SELECT SERVICE")
		add("Choose the registered service providing configuration and release trust.")
		add("")
		for index, head := range m.headChoices {
			selectRow(head.ID+" ("+head.Service+")", index == m.cursor)
		}
	case form:
		heading(title(component.ID) + " / " + actionLabel(m.pending.Kind))
		add("Service: " + m.pending.HeadID)
		if m.choice == "enroll" {
			add("Create a one-time setup code in Saturn > Synchronization.")
		}
		if m.choice == "connect-bot" {
			add("Bot registration is separate from service and Telegram-user linking.")
		}
		add("")
		for i, field := range m.fields {
			add(field.label)
			input := field.input
			input.Width = max(1, min(100, m.width-9))
			input.SetCursor(input.Position())
			rows = append(rows, row{text: input.View(), focus: m.cursor == i, raw: true})
			add("")
		}
		if m.notice != "" {
			add(m.notice)
		}
		selectRow("Continue to confirmation", m.cursor == len(m.fields))
	case confirm:
		heading("CONFIRM / " + title(m.pending.Component))
		add("Action: " + actionLabel(m.pending.Kind))
		add("Service: " + m.pending.HeadID)
		if m.pending.Version != "" {
			add("Exact version: " + m.pending.Version)
		}
		selectRow("Cancel", m.cursor == 0)
		selectRow("Confirm "+strings.ToLower(actionLabel(m.pending.Kind)), m.cursor == 1)
		add("")
		if m.pending.Kind == "enroll" {
			add("The agent will be installed if needed and linked using the one-time code.")
		}
		if m.pending.Kind == "connect-bot" {
			add("Bot alias: " + m.pending.Alias)
			add("Gryphon will verify and retain the bot credential.")
		}
		if m.pending.Kind == "install" {
			add("Install or verify the shared helper using the configured signed release.")
		}
		if m.pending.Kind == "update" {
			add("The shared component may briefly restart. Other local services use this same component.")
		}
		add("Accepted work continues if this terminal disconnects.")
		if m.demo {
			add("DEMO: this only simulates the operation.")
		}
		if m.notice != "" {
			add(m.notice)
		}
	case result:
		heading(title(component.ID) + " / RESULT")
		if m.working {
			add("Please wait; the interface remains responsive.")
			add("")
		}
		if m.activeJob != nil {
			job := m.activeJob
			add("State: " + job.State)
			add(job.Summary)
			add("Job: " + job.ID)
			add("Service: " + job.HeadID)
			if job.Version != "" {
				add("Version: " + job.Version)
			}
			if !job.Finished {
				add("Status refreshes automatically.")
			}
		} else if m.candidate != nil && !m.working {
			add("Service: " + m.candidate.HeadID)
			add("Installed: " + m.candidate.Installed)
			if m.candidate.UpdateAvailable {
				add("Available: " + m.candidate.Available)
				add("Installation will independently verify signatures and checksums.")
				add("")
				heading("Enter: review update confirmation")
			} else {
				add("Already up to date.")
			}
		} else {
			for _, line := range m.resultLines {
				add(line)
			}
		}
		if m.errorText != "" {
			add("")
			add(m.errorText)
		}
	case jobs:
		heading(title(component.ID) + " / OPERATIONS")
		list := m.visibleJobs()
		if len(list) == 0 {
			add("No retained operations for this component.")
		}
		for index, job := range list {
			selectRow(job.UpdatedAt.Local().Format("01-02 15:04")+"  "+job.State+"  "+job.HeadID, index == m.cursor)
		}
		if !m.connected {
			add("Offline: showing the last observed history.")
		}
	case bots:
		heading("GRYPHON / REGISTERED BOTS")
		if len(m.botList) == 0 {
			add("No registered bots.")
		}
		for _, bot := range m.botList {
			add(bot.Alias + "  @" + bot.Username + "  " + bot.State)
		}
	case help:
		heading("HELP / " + title(component.ID))
		for _, line := range []string{
			"Use Up/Down and Enter. Esc returns; Ctrl+C exits. Tab moves between form fields.",
			"Refresh status observes local process state and the running API version. An active unit alone does not mean that its API is healthy.",
			"If the operator API is offline: inspect sudo systemctl status updater.service. An older daemon may need the matching Updater release and systemd unit.",
			"For component failures: inspect sudo systemctl status " + component.ID + ".service on this host. Review configuration without copying credentials into logs.",
			"Release checks need a valid registered service and available Kernel/Volt. Installation independently verifies the selected signed release.",
			"Neptune: create a setup code in Saturn > Synchronization. Verify the loopback export URL for the selected service. Schedules remain in Saturn.",
			"Gryphon: connecting a bot does not link a service function or authorize a Telegram user. Those remain separate operations.",
			"Operation IDs survive console/SSH loss. Reopen operation history after reconnecting. Never assume a lost response means no operation started.",
			"After Updater self-update, the console reconnects. Relaunch updater tui to use the new UI version. Host/daemon interruption follows existing recovery rules.",
			"Termius: use an ordinary SSH terminal with a PTY. Resize or rotate the screen freely. Special fonts and mouse support are unnecessary.",
			"Use updater tui --no-color for monochrome. Use updater tui --demo to explore synthetic data without privileges or service access.",
		} {
			add(line)
			add("")
		}
	}
	if m.snapshot.Notice != "" && m.screen == services {
		add("")
		add(m.snapshot.Notice)
	}
	return rows
}

func actionLabel(kind string) string {
	switch kind {
	case "install":
		return "Install helper"
	case "enroll":
		return "Initialize Neptune"
	case "connect-bot":
		return "Connect bot"
	case "update":
		return "Update component"
	}
	return kind
}
