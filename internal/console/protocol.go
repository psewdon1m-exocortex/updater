// Package console is the bounded, secret-free operator view shared by the
// local API and the terminal client. It does not own service configuration.
package console

import (
	"context"
	"errors"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

const Protocol = 2

var setupCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32}$`)
var botAliasPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,47}$`)
var botTokenPattern = regexp.MustCompile(`^[0-9]{5,}:[A-Za-z0-9_-]{20,200}$`)
var exactVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type Component struct {
	ID          string `json:"id"`
	Installed   bool   `json:"installed"`
	Process     string `json:"process"`
	Health      string `json:"health"`
	Version     string `json:"version,omitempty"`
	Detail      string `json:"detail"`
	FallbackURL string `json:"fallback_url,omitempty"`
}

type Head struct {
	ID        string   `json:"id"`
	Service   string   `json:"service"`
	Helpers   []string `json:"helpers"`
	ExportURL string   `json:"export_url,omitempty"`
	Problem   string   `json:"problem,omitempty"`
}

type Job struct {
	ID              string    `json:"id"`
	RequestID       string    `json:"request_id"`
	HeadID          string    `json:"head_id"`
	Component       string    `json:"component"`
	State           string    `json:"state"`
	Version         string    `json:"version,omitempty"`
	Summary         string    `json:"summary"`
	UpdatedAt       time.Time `json:"updated_at"`
	Finished        bool      `json:"finished"`
	PairCommand     string    `json:"pair_command,omitempty"`
	PairExpiresAt   string    `json:"pair_expires_at,omitempty"`
	PairBotUsername string    `json:"pair_bot_username,omitempty"`
	WindowLeaseID   string    `json:"window_lease_id,omitempty"`
}

type Snapshot struct {
	Protocol           int         `json:"protocol"`
	Host               string      `json:"host"`
	ObservedAt         time.Time   `json:"observed_at"`
	Components         []Component `json:"components"`
	Heads              []Head      `json:"heads"`
	Jobs               []Job       `json:"jobs"`
	Notice             string      `json:"notice,omitempty"`
	KernelURL          string      `json:"kernel_url,omitempty"`
	KernelTokenFile    string      `json:"kernel_token_file,omitempty"`
	HostID             string      `json:"host_id,omitempty"`
	KernelAccess       string      `json:"kernel_access,omitempty"`
	RecoveryGatewayURL string      `json:"recovery_gateway_url,omitempty"`
	RecoveryConfigured bool        `json:"recovery_configured,omitempty"`
	RecoveryServices   []string    `json:"recovery_services,omitempty"`
}

type Candidate struct {
	Component       string `json:"component"`
	HeadID          string `json:"head_id"`
	Installed       string `json:"installed"`
	Available       string `json:"available"`
	UpdateAvailable bool   `json:"update_available"`
	SourceOrigin    string `json:"source_origin,omitempty"`
	SourceReason    string `json:"source_reason,omitempty"`
}

type Action struct {
	Component       string         `json:"component"`
	Kind            string         `json:"kind"`
	HeadID          string         `json:"head_id"`
	RequestID       string         `json:"request_id"`
	Version         string         `json:"version,omitempty"`
	ExportURL       string         `json:"export_url,omitempty"`
	SetupCode       string         `json:"setup_code,omitempty"`
	Alias           string         `json:"alias,omitempty"`
	BotToken        string         `json:"bot_token,omitempty"`
	RepositoryURL   string         `json:"repository_url,omitempty"`
	KernelURL       string         `json:"kernel_url,omitempty"`
	KernelTokenFile string         `json:"kernel_token_file,omitempty"`
	HostID          string         `json:"host_id,omitempty"`
	Wyvern          *WyvernInput   `json:"wyvern,omitempty"`
	PublicKey       string         `json:"public_key,omitempty"`
	Minutes         int            `json:"minutes,omitempty"`
	Recovery        *RecoveryInput `json:"recovery,omitempty"`
}

type RecoveryInput struct {
	KeyPath         string            `json:"key_path,omitempty"`
	GatewayURL      string            `json:"gateway_url,omitempty"`
	EnrollmentCodes map[string]string `json:"enrollment_codes,omitempty"`
	Passphrase      string            `json:"passphrase,omitempty"`
	ArchivePath     string            `json:"archive_path,omitempty"`
	Service         string            `json:"service,omitempty"`
	Confirmation    string            `json:"confirmation,omitempty"`
}

type Bot struct {
	Alias    string `json:"alias"`
	Username string `json:"username"`
	State    string `json:"state"`
	Paired   bool   `json:"paired"`
}

type Backend interface {
	Snapshot(context.Context) (Snapshot, error)
	Check(context.Context, string, string) (Candidate, error)
	Act(context.Context, Action) (Job, error)
	Bots(context.Context) ([]Bot, error)
}

func ValidateAction(a Action) error {
	recoveryService := a.Component == "updater" || a.Component == "neptune" || a.Component == "gryphon" || a.Component == "wyvern"
	if recoveryService && strings.HasPrefix(a.Kind, "recovery-") {
		if a.HeadID != "" || a.Version != "" || a.ExportURL != "" || a.SetupCode != "" || a.Alias != "" || a.BotToken != "" || a.RepositoryURL != "" || a.KernelURL != "" || a.KernelTokenFile != "" || a.HostID != "" || a.Wyvern != nil || a.PublicKey != "" || a.Minutes != 0 || a.Recovery == nil {
			return errors.New("Unexpected recovery action fields")
		}
		recovery := a.Recovery
		switch a.Kind {
		case "recovery-configure":
			u, err := url.Parse(recovery.GatewayURL)
			if (recovery.GatewayURL != "" && (err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/"))) || recovery.KeyPath != "" || len(recovery.GatewayURL) > 512 || recovery.Passphrase != "" || recovery.ArchivePath != "" || recovery.Service != a.Component || recovery.Confirmation != "" || len(recovery.EnrollmentCodes) != 1 {
				return errors.New("Enter this service's backup setup code; an HTTPS origin override is optional")
			}
			if !setupCodePattern.MatchString(recovery.EnrollmentCodes[a.Component]) {
				return errors.New("This recovery service requires one 32-character setup code")
			}
			return nil
		case "recovery-export":
			if recovery.Passphrase != "" || recovery.KeyPath != "" || recovery.GatewayURL != "" || len(recovery.EnrollmentCodes) != 0 || recovery.ArchivePath != "" || recovery.Service != a.Component || recovery.Confirmation != "CREATE "+strings.ToUpper(a.Component)+" RECOVERY" {
				return errors.New("Confirm the selected service recovery export; its encryption key is managed automatically")
			}
			return nil
		case "recovery-key-export":
			if !filepath.IsAbs(recovery.KeyPath) || len(recovery.KeyPath) > 1024 || strings.ContainsAny(recovery.KeyPath, "\r\n\x00") || recovery.Passphrase != "" || recovery.ArchivePath != "" || recovery.GatewayURL != "" || len(recovery.EnrollmentCodes) != 0 || recovery.Service != a.Component || recovery.Confirmation != "" {
				return errors.New("Select an absolute path for a new recovery key file")
			}
			return nil
		case "recovery-restore":
			if !filepath.IsAbs(recovery.ArchivePath) || strings.ContainsAny(recovery.ArchivePath, "\r\n\x00") || len(recovery.ArchivePath) > 1024 || recovery.Service != a.Component || (recovery.Passphrase != "" && (len(recovery.Passphrase) < 16 || len(recovery.Passphrase) > 1024 || recovery.KeyPath != "")) || (recovery.KeyPath != "" && (!filepath.IsAbs(recovery.KeyPath) || len(recovery.KeyPath) > 1024 || strings.ContainsAny(recovery.KeyPath, "\r\n\x00"))) || recovery.Confirmation != "RESTORE "+strings.ToUpper(recovery.Service) || recovery.GatewayURL != "" || len(recovery.EnrollmentCodes) != 0 {
				return errors.New("Select an absolute scoped archive path and type its exact RESTORE SERVICE confirmation")
			}
			return nil
		}
		return errors.New("Unsupported recovery action")
	}
	if a.Recovery != nil {
		return errors.New("Unexpected recovery input")
	}
	if a.Component == "window" && (a.Kind == "pair" || a.Kind == "open" || a.Kind == "revoke" || a.Kind == "repair") {
		if a.HeadID != "" || a.Version != "" || a.ExportURL != "" || a.SetupCode != "" || a.Alias != "" || a.BotToken != "" || a.RepositoryURL != "" || a.KernelURL != "" || a.KernelTokenFile != "" || a.HostID != "" || a.Wyvern != nil {
			return errors.New("Unexpected Window action fields")
		}
		switch a.Kind {
		case "pair":
			if a.Minutes != 0 || len(a.PublicKey) > 512 || !strings.HasPrefix(a.PublicKey, "ssh-ed25519 ") {
				return errors.New("Enter one ssh-ed25519 public key")
			}
		case "open":
			if a.PublicKey != "" || a.Minutes < 1 || a.Minutes > 120 {
				return errors.New("Window duration must be 1–120 minutes")
			}
		case "revoke", "repair":
			if a.PublicKey != "" || a.Minutes != 0 {
				return errors.New("Unexpected Window action fields")
			}
		}
		return nil
	}
	if a.Component == "window" && (a.Kind == "install" || a.Kind == "update") {
		if a.HeadID != "" || a.PublicKey != "" || a.Minutes != 0 || a.ExportURL != "" || a.SetupCode != "" || a.Alias != "" || a.BotToken != "" || a.RepositoryURL != "" || a.KernelURL != "" || a.KernelTokenFile != "" || a.HostID != "" || a.Wyvern != nil {
			return errors.New("Unexpected Window release fields")
		}
		if a.Kind == "install" && a.Version == "" || a.Kind == "update" && exactVersionPattern.MatchString(a.Version) {
			return nil
		}
		return errors.New("Check an exact Window release before updating")
	}
	if a.PublicKey != "" || a.Minutes != 0 {
		return errors.New("Unexpected Window action fields")
	}
	if a.Kind != "set-kernel" && (a.KernelURL != "" || a.KernelTokenFile != "" || a.HostID != "") {
		return errors.New("Unexpected host Kernel connection fields")
	}
	if a.Kind == "set-kernel" {
		u, err := url.Parse(a.KernelURL)
		if a.Component != "updater" || a.HeadID != "" || a.Version != "" || a.RepositoryURL != "" || a.Wyvern != nil || a.ExportURL != "" || a.SetupCode != "" || a.Alias != "" || a.BotToken != "" ||
			err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(a.KernelURL) > 512 ||
			!strings.HasPrefix(a.KernelTokenFile, "/") || strings.ContainsAny(a.KernelTokenFile, "\r\n\x00") || len(a.KernelTokenFile) > 512 ||
			!wyvernIdentifier.MatchString(a.HostID) {
			return errors.New("Invalid host Kernel machine connection")
		}
		return nil
	}
	if a.Kind != "set-source" && a.RepositoryURL != "" {
		return errors.New("Unexpected release source field")
	}
	if a.Kind == "set-source" {
		if a.HeadID != "" || a.Version != "" || a.Wyvern != nil || a.ExportURL != "" || a.SetupCode != "" || a.Alias != "" || a.BotToken != "" ||
			(a.Component != "updater" && a.Component != "neptune" && a.Component != "gryphon" && a.Component != "wyvern" && a.Component != "window") {
			return errors.New("Invalid host release source action")
		}
		u, err := url.Parse(a.RepositoryURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(a.RepositoryURL) > 512 || strings.Trim(u.Path, "/") == "" {
			return errors.New("Enter an HTTPS repository URL without credentials, query or fragment")
		}
		return nil
	}
	if a.Component == "wyvern" {
		if (a.Kind == "install" || a.Kind == "update") && a.Wyvern == nil && a.ExportURL == "" && a.SetupCode == "" && a.Alias == "" && a.BotToken == "" && ((a.Kind == "install" && a.Version == "") || (a.Kind == "update" && a.HeadID == "" && exactVersionPattern.MatchString(a.Version))) {
			return nil
		}
		if a.Wyvern != nil {
			return validateWyvernAction(a)
		}
		if (a.Kind == "reload" || a.Kind == "drain" || a.Kind == "resume" || a.Kind == "repair") && a.HeadID == "" && a.Version == "" && a.ExportURL == "" && a.SetupCode == "" && a.Alias == "" && a.BotToken == "" {
			return nil
		}
		return errors.New("Unsupported Wyvern operation or unexpected fields")
	}
	if a.Wyvern != nil {
		return errors.New("Unexpected Wyvern configuration")
	}
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
