package component

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// InstalledVersion queries the running daemon, not the release catalogue or a
// caller-supplied version. This also detects an old process after replacement.
func InstalledVersion(kind string) (string, error) {
	socket, endpoint, schema, err := runningVersionEndpoint(kind)
	if err != nil {
		return "", err
	}
	return readRunningVersion(socket, endpoint, schema)
}

func runningVersionEndpoint(kind string) (string, string, string, error) {
	switch kind {
	case "wyvern":
		return "/run/wyvern-admin/admin.sock", "/v1/status", "exocortex.wyvern.status.v1", nil
	case "neptune":
		return neptuneSocket, "/v1/health", "", nil
	case "gryphon":
		// Released Gryphon versions report their in-process version on the
		// admin status socket; client health intentionally contains only status.
		return "/run/gryphon-admin/admin.sock", "/v1/status", "exocortex.gryphon.status.v1", nil
	default:
		return "", "", "", errors.New("unknown helper")
	}
}

func readRunningVersion(socket, endpoint, schema string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	request, _ := http.NewRequestWithContext(ctx, "GET", "http://helper.local"+endpoint, nil)
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var health struct {
		Version string `json:"version"`
		Schema  string `json:"schema"`
	}
	if response.StatusCode != 200 {
		return "", errors.New("helper health check failed")
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 2*1024*1024)).Decode(&health); err != nil {
		return "", err
	}
	if schema != "" && health.Schema != schema {
		return "", errors.New("helper version response has an unexpected schema")
	}
	if !neptuneVersion.MatchString(health.Version) {
		return "", errors.New("helper did not report its running version")
	}
	return health.Version, nil
}

func verifyRunningComponent(kind, expected string) error {
	current, err := InstalledVersion(kind)
	if err != nil {
		return err
	}
	if strings.TrimSuffix(current, "-dev") != expected {
		return errors.New("running component version differs from selected release")
	}
	return nil
}
