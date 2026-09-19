package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
)

type WyvernInput struct {
	KernelURL       string   `json:"kernel_url,omitempty"`
	AccessKey       string   `json:"access_key,omitempty"`
	InstanceID      string   `json:"instance_id,omitempty"`
	AdapterID       string   `json:"adapter_id,omitempty"`
	Name            string   `json:"name,omitempty"`
	Model           string   `json:"model,omitempty"`
	APIKey          string   `json:"api_key,omitempty"`
	Profile         string   `json:"profile,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	MaxOutput       int      `json:"max_output_tokens,omitempty"`
	ClientID        string   `json:"client_id,omitempty"`
	AllowedAdapters []string `json:"allowed_adapters,omitempty"`
	Revision        int      `json:"expected_revision,omitempty"`
}

var wyvernIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var wyvernModel = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

func validateWyvernAction(a Action) error {
	i := a.Wyvern
	if a.HeadID != "" || a.Version != "" || a.ExportURL != "" || a.SetupCode != "" || a.Alias != "" || a.BotToken != "" {
		return errors.New("Unexpected host operation fields")
	}
	allowed := map[string]bool{}
	fields := func(names ...string) {
		for _, name := range names {
			allowed[name] = true
		}
	}
	bad := errors.New("Invalid Wyvern operation fields")
	switch a.Kind {
	case "connect-kernel":
		fields("kernel_url", "access_key", "instance_id")
		u, err := url.Parse(i.KernelURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || i.AccessKey == "" || i.InstanceID != "" && !wyvernIdentifier.MatchString(i.InstanceID) {
			return bad
		}
	case "adapter-put", "profile-put":
		fields("adapter_id", "model", "profile", "capabilities", "max_output_tokens", "expected_revision")
		if a.Kind == "adapter-put" {
			fields("name", "api_key")
			if i.Name == "" || len(i.Name) > 100 || len(i.APIKey) > 4096 {
				return bad
			}
		}
		if !wyvernIdentifier.MatchString(i.AdapterID) || !wyvernModel.MatchString(i.Model) || i.Profile != "" && !wyvernIdentifier.MatchString(i.Profile) || i.MaxOutput < 1 || i.MaxOutput > 65536 || len(i.Capabilities) < 1 || len(i.Capabilities) > 9 || i.Revision < 1 {
			return bad
		}
	case "adapter-disable", "adapter-enable", "adapter-delete":
		fields("adapter_id", "expected_revision")
		if !wyvernIdentifier.MatchString(i.AdapterID) || i.Revision < 1 {
			return bad
		}
	case "client-grant":
		fields("client_id", "allowed_adapters", "expected_revision")
		if !wyvernIdentifier.MatchString(i.ClientID) || len(i.AllowedAdapters) > 16 || i.Revision < 1 {
			return bad
		}
		for _, id := range i.AllowedAdapters {
			if !wyvernIdentifier.MatchString(id) {
				return bad
			}
		}
	case "client-revoke":
		fields("client_id", "expected_revision")
		if !wyvernIdentifier.MatchString(i.ClientID) || i.Revision < 1 {
			return bad
		}
	default:
		return errors.New("Unsupported Wyvern management action")
	}
	body, _ := json.Marshal(i)
	var present map[string]any
	_ = json.Unmarshal(body, &present)
	for name := range present {
		if !allowed[name] {
			return bad
		}
	}
	return nil
}

type WyvernEditState struct {
	Revision int `json:"revision"`
}
type WyvernEditBackend interface {
	WyvernEdit(context.Context) (WyvernEditState, error)
}

func (c *Client) WyvernEdit(ctx context.Context) (WyvernEditState, error) {
	var state WyvernEditState
	err := c.request(ctx, "GET", "/v1/wyvern/config", nil, &state)
	return state, err
}
