package component

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"time"
	"updater/internal/config"
)

func gryphonLocal(ctx context.Context, socket, method, route string, body any) (map[string]any, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, method, "http://gryphon.local"+route, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Transport: transport, Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return nil, errors.New("Gryphon is unavailable")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(data) > 65536 {
		return nil, errors.New("invalid Gryphon response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, errors.New("Gryphon rejected the operation; verify bot credentials and Kernel discovery")
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func gryphonHealth(ctx context.Context) error {
	_, err := gryphonLocal(ctx, gryphonSocket, "GET", "/v1/health", nil)
	return err
}

// No arbitrary admin route or service id may cross the head boundary.
func ConnectGryphonBot(runtime config.Runtime, headID, alias, token string) error {
	head, err := config.LoadHead(runtime, headID)
	if err != nil {
		return err
	}
	if head.Service != "saturn" {
		return errors.New("this head does not consume Gryphon")
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{1,47}$`).MatchString(alias) || !regexp.MustCompile(`^[0-9]{5,}:[A-Za-z0-9_-]{20,200}$`).MatchString(token) {
		return errors.New("invalid bot alias or token")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, err = gryphonLocal(ctx, "/run/gryphon-admin/admin.sock", "POST", "/v1/bots", map[string]string{"alias": alias, "botToken": token})
	return err
}
