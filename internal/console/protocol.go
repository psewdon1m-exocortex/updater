// Package console is the bounded, secret-free operator view shared by the
// local API and the terminal client. It does not own service configuration.
package console

import (
	"context"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

const Protocol = 1

var setupCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32}$`)
var botAliasPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,47}$`)
var botTokenPattern = regexp.MustCompile(`^[0-9]{5,}:[A-Za-z0-9_-]{20,200}$`)
var exactVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type Component struct {
	ID        string `json:"id"`
	Installed bool   `json:"installed"`
	Process   string `json:"process"`
	Health    string `json:"health"`
	Version   string `json:"version,omitempty"`
	Detail    string `json:"detail"`
}

type Head struct {
	ID        string   `json:"id"`
	Service   string   `json:"service"`
	Helpers   []string `json:"helpers"`
	ExportURL string   `json:"export_url,omitempty"`
	Problem   string   `json:"problem,omitempty"`
}

type Job struct {
	ID        string    `json:"id"`
	RequestID string    `json:"request_id"`
	HeadID    string    `json:"head_id"`
	Component string    `json:"component"`
	State     string    `json:"state"`
	Version   string    `json:"version,omitempty"`
	Summary   string    `json:"summary"`
	UpdatedAt time.Time `json:"updated_at"`
	Finished  bool      `json:"finished"`
}

type Snapshot struct {
	Protocol   int         `json:"protocol"`
	Host       string      `json:"host"`
	ObservedAt time.Time   `json:"observed_at"`
	Components []Component `json:"components"`
	Heads      []Head      `json:"heads"`
	Jobs       []Job       `json:"jobs"`
	Notice     string      `json:"notice,omitempty"`
}

type Candidate struct {
	Component       string `json:"component"`
	HeadID          string `json:"head_id"`
	Installed       string `json:"installed"`
	Available       string `json:"available"`
	UpdateAvailable bool   `json:"update_available"`
}

type Action struct {
	Component string `json:"component"`
	Kind      string `json:"kind"`
	HeadID    string `json:"head_id"`
	RequestID string `json:"request_id"`
	Version   string `json:"version,omitempty"`
	ExportURL string `json:"export_url,omitempty"`
	SetupCode string `json:"setup_code,omitempty"`
	Alias     string `json:"alias,omitempty"`
	BotToken  string `json:"bot_token,omitempty"`
}

type Bot struct {
	Alias    string `json:"alias"`
	Username string `json:"username"`
	State    string `json:"state"`
}

type Backend interface {
	Snapshot(context.Context) (Snapshot, error)
	Check(context.Context, string, string) (Candidate, error)
	Act(context.Context, Action) (Job, error)
	Bots(context.Context) ([]Bot, error)
}

func ValidateAction(a Action) error {
	if a.Component != "updater" && a.Component != "neptune" && a.Component != "gryphon" {
		return errors.New("Unsupported component")
	}
	switch a.Kind {
	case "update":
		if !exactVersionPattern.MatchString(a.Version) {
			return errors.New("Check for an exact stable release before updating")
		}
		if a.ExportURL == "" && a.SetupCode == "" && a.Alias == "" && a.BotToken == "" {
			return nil
		}
	case "install":
		if a.Component != "updater" && a.Version == "" && a.ExportURL == "" && a.SetupCode == "" && a.Alias == "" && a.BotToken == "" {
			return nil
		}
	case "enroll":
		if a.Component != "neptune" || a.Version != "" || a.Alias != "" || a.BotToken != "" {
			break
		}
		if !setupCodePattern.MatchString(a.SetupCode) {
			return errors.New("Use the original 32-character Saturn setup code")
		}
		u, err := url.Parse(a.ExportURL)
		if err != nil || len(a.ExportURL) > 512 || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path == "" {
			return errors.New("Use a loopback HTTP export URL without credentials, query or fragment")
		}
		ip := net.ParseIP(u.Hostname())
		if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return errors.New("The export URL must point to localhost or a loopback IP")
		}
		return nil
	case "connect-bot":
		if a.Component != "gryphon" || a.Version != "" || a.ExportURL != "" || a.SetupCode != "" {
			break
		}
		if !botAliasPattern.MatchString(a.Alias) {
			return errors.New("Bot alias: 2-48 lowercase letters, digits or hyphens; start with a letter")
		}
		if !botTokenPattern.MatchString(a.BotToken) {
			return errors.New("Enter the Telegram bot token in its original format")
		}
		return nil
	}
	return errors.New("Unsupported action or unexpected action fields")
}

// Text is applied before rendering untrusted labels. In particular OSC 52,
// cursor movement, C0/C1 controls and bidi overrides are never terminal input.
func Text(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, ansi.Strip(value))
	runes := []rune(value)
	if len(runes) > 300 {
		value = string(runes[:300]) + "..."
	}
	return value
}

func Eligible(head Head, component string) bool {
	if head.Problem != "" {
		return false
	}
	if component == "updater" {
		return true
	}
	for _, helper := range head.Helpers {
		if helper == component {
			return true
		}
	}
	return false
}
