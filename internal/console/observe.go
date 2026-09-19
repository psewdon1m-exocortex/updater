package console

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

var observedVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[.-][a-zA-Z0-9.-]+)?$`)

// LocalComponents can run even when the operator API is down. It only observes
// fixed units/paths; no service is started or modified by a status refresh.
func LocalComponents(ctx context.Context, updaterVersion string) []Component {
	items := []Component{{ID: "updater"}, {ID: "neptune"}, {ID: "gryphon"}, {ID: "wyvern"}}
	var group sync.WaitGroup
	for index := range items {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			items[i] = observe(probeCtx, items[i].ID)
		}(index)
	}
	group.Wait()
	if updaterVersion != "" {
		items[0].Installed, items[0].Health, items[0].Version = true, "ready", Text(updaterVersion)
		items[0].Detail = "Operator API is responding. Updates run independently of this console."
	}
	return items
}

func observe(ctx context.Context, kind string) Component {
	item := Component{ID: kind, Process: "unknown", Health: "unavailable"}
	binary, socket, route, schema := "/usr/bin/updater", "/run/exocortex/updater.sock", "/v1/health", ""
	if kind == "neptune" {
		binary, socket = "/usr/local/lib/neptune/neptuned", "/run/neptune/neptuned.sock"
	}
	if kind == "gryphon" {
		binary, socket, route, schema = "/usr/local/lib/gryphon/app/dist/main.js", "/run/gryphon-admin/admin.sock", "/v1/status", "exocortex.gryphon.status.v1"
	}
	if kind == "wyvern" {
		binary, socket, route, schema = "/etc/exocortex/units/wyvern.service", WyvernAdminSocket, "/v1/status", "exocortex.wyvern.status.v1"
	}
	_, statErr := os.Stat(binary)
	item.Installed = statErr == nil
	output, err := exec.CommandContext(ctx, "systemctl", "show", "--property=ActiveState", "--value", kind+".service").Output()
	if err == nil {
		switch value := strings.TrimSpace(string(output)); value {
		case "active", "inactive", "failed", "activating", "deactivating", "reloading":
			item.Process = value
		}
	}
	if kind == "wyvern" {
		var health WyvernStatus
		if err := LocalJSON(ctx, socket, route, &health); err == nil && ValidWyvernStatus(health) {
			item.Installed, item.Health, item.Version = true, health.State, health.Version
			item.Detail = "Wyvern is responding. Adapter selection and readiness are tracked per client."
			if !health.ConfigurationLoaded {
				item.Detail = "Wyvern is installed; connect Kernel and configure an Adapter before using LLM functions."
			}
			if health.Drain {
				item.Detail = "New LLM requests are paused for all clients. Active requests can finish."
			}
			return item
		}
	}
	var health struct {
		Schema  string `json:"schema"`
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	err = LocalJSON(ctx, socket, route, &health)
	if kind != "wyvern" && err == nil && observedVersion.MatchString(health.Version) && (schema == "" && health.Status == "ok" || schema != "" && health.Schema == schema) {
		item.Installed, item.Health, item.Version = true, "ready", health.Version
		item.Detail = "Local API is responding with its running version."
	} else if !item.Installed && errors.Is(statErr, os.ErrNotExist) {
		item.Health, item.Detail = "not installed", "No managed executable was found on this host."
	} else {
		item.Detail = "Local API did not pass its health check. Inspect the unit and configuration."
	}
	return item
}

func LocalJSON(ctx context.Context, socket, route string, target any) error {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://updater.local"+route, nil)
	response, err := (&http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return errors.New("Local service unavailable")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 256*1024+1))
	if err != nil || len(data) > 256*1024 || response.StatusCode != 200 {
		return errors.New("Invalid local service response")
	}
	return json.Unmarshal(data, target)
}
