package console

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

const WyvernAdminSocket = "/run/wyvern-admin/admin.sock"

type WyvernStatus struct {
	Schema              string `json:"schema"`
	Service             string `json:"service"`
	Version             string `json:"version"`
	APIVersion          int    `json:"api_version"`
	InstanceID          string `json:"instance_id"`
	Generation          string `json:"generation"`
	Ready               bool   `json:"ready"`
	ConfigurationLoaded bool   `json:"configuration_loaded"`
	State               string `json:"state"`
	ConfigError         string `json:"config_error"`
	Drain               bool   `json:"drain"`
	ActiveRequests      int    `json:"active_requests"`
}
type WyvernProfile struct {
	Name         string   `json:"name"`
	Model        string   `json:"model"`
	Capabilities []string `json:"capabilities"`
}
type WyvernAdapter struct {
	ID       string          `json:"adapter_id"`
	Name     string          `json:"name"`
	Driver   string          `json:"driver"`
	Enabled  bool            `json:"enabled"`
	Profiles []WyvernProfile `json:"profiles"`
}
type WyvernBinding struct {
	AdapterID string `json:"adapter_id"`
	Profile   string `json:"profile"`
}
type WyvernClient struct {
	ID              string                   `json:"client_id"`
	Enabled         bool                     `json:"enabled"`
	AllowedAdapters []string                 `json:"allowed_adapters"`
	Bindings        map[string]WyvernBinding `json:"bindings"`
}
type WyvernCatalog struct {
	Schema     string          `json:"schema"`
	Generation string          `json:"generation"`
	Adapters   []WyvernAdapter `json:"adapters"`
	Clients    []WyvernClient  `json:"clients"`
}
type WyvernView struct {
	Status  WyvernStatus  `json:"status"`
	Catalog WyvernCatalog `json:"catalog"`
}

func ValidWyvernStatus(status WyvernStatus) bool {
	if status.Schema != "exocortex.wyvern.status.v1" || status.Service != "wyvern" || status.APIVersion != 1 || !observedVersion.MatchString(status.Version) || status.ActiveRequests < 0 || status.ActiveRequests > 256 {
		return false
	}
	switch status.State {
	case "ready":
		return status.Ready && status.ConfigurationLoaded && !status.Drain
	case "unconfigured":
		return !status.Ready && !status.ConfigurationLoaded
	case "draining":
		return !status.Ready && status.Drain
	case "degraded":
		return status.ConfigurationLoaded && !status.Drain
	}
	return false
}

func ReadWyvern(ctx context.Context, socket string) (WyvernView, error) {
	for attempt := 0; attempt < 2; attempt++ {
		var result WyvernView
		if err := LocalJSON(ctx, socket, "/v1/status", &result.Status); err != nil || !ValidWyvernStatus(result.Status) {
			return result, errors.New("Wyvern status is unavailable or incompatible")
		}
		if err := LocalJSON(ctx, socket, "/v1/catalog", &result.Catalog); err != nil || result.Catalog.Schema != "exocortex.wyvern.catalog.v1" || len(result.Catalog.Adapters) > 16 || len(result.Catalog.Clients) > 256 {
			return WyvernView{}, errors.New("Wyvern Adapter catalog is unavailable or incompatible")
		}
		if result.Catalog.Generation != result.Status.Generation {
			continue
		}
		// Whitelisted typed metadata is re-encoded. Credential fields, even if
		// returned by a faulty producer, cannot cross the operator boundary.
		result.Status.InstanceID = Text(result.Status.InstanceID)
		result.Status.ConfigError = Text(result.Status.ConfigError)
		for i := range result.Catalog.Adapters {
			a := &result.Catalog.Adapters[i]
			if len(a.Profiles) > 8 {
				return WyvernView{}, errors.New("Invalid Wyvern profiles")
			}
			a.ID, a.Name, a.Driver = Text(a.ID), Text(a.Name), Text(a.Driver)
			for j := range a.Profiles {
				p := &a.Profiles[j]
				if len(p.Capabilities) > 16 {
					return WyvernView{}, errors.New("Invalid Wyvern capabilities")
				}
				p.Name, p.Model = Text(p.Name), Text(p.Model)
				for k := range p.Capabilities {
					p.Capabilities[k] = Text(p.Capabilities[k])
				}
			}
		}
		for i := range result.Catalog.Clients {
			c := &result.Catalog.Clients[i]
			if len(c.AllowedAdapters) > 16 || len(c.Bindings) > 32 {
				return WyvernView{}, errors.New("Invalid Wyvern client bindings")
			}
			c.ID = Text(c.ID)
			for j := range c.AllowedAdapters {
				c.AllowedAdapters[j] = Text(c.AllowedAdapters[j])
			}
			bindings := map[string]WyvernBinding{}
			for function, binding := range c.Bindings {
				bindings[Text(function)] = WyvernBinding{Text(binding.AdapterID), Text(binding.Profile)}
			}
			c.Bindings = bindings
		}
		return result, nil
	}
	return WyvernView{}, errors.New("Wyvern configuration changed during observation; refresh status")
}

func ControlWyvern(ctx context.Context, socket, kind string) (WyvernStatus, error) {
	route, payload := "/v1/reload", []byte("{}")
	switch kind {
	case "reload":
	case "drain", "resume":
		route = "/v1/drain"
		payload, _ = json.Marshal(map[string]bool{"enabled": kind == "drain"})
	default:
		return WyvernStatus{}, errors.New("Unsupported Wyvern operation")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://wyvern.local"+route, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return WyvernStatus{}, errors.New("Wyvern operation unavailable; inspect status before retrying")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 32769))
	var status WyvernStatus
	if err != nil || len(body) > 32768 || response.StatusCode != 200 || json.Unmarshal(body, &status) != nil || !ValidWyvernStatus(status) {
		return WyvernStatus{}, errors.New("Wyvern did not verify the operation; inspect status and configuration")
	}
	if kind == "drain" && !status.Drain || kind == "resume" && status.Drain || kind == "reload" && !status.ConfigurationLoaded {
		return WyvernStatus{}, errors.New("Wyvern returned an unexpected operation state")
	}
	return status, nil
}

type WyvernBackend interface {
	Wyvern(context.Context) (WyvernView, error)
}

func (c *Client) Wyvern(ctx context.Context) (WyvernView, error) {
	var result WyvernView
	err := c.request(ctx, "GET", "/v1/wyvern", nil, &result)
	return result, err
}
