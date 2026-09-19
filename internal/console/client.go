package console

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type Client struct{ http *http.Client }

func NewClient(socket string) *Client {
	return &Client{http: &http.Client{
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
		}},
		Timeout:       50 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) request(ctx context.Context, method, path string, input, output any) error {
	var body bytes.Buffer
	if input != nil {
		if err := json.NewEncoder(&body).Encode(input); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://updater.local"+path, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return errors.New("Operator connection unavailable. Check updater.service and the installed version; reconnecting automatically")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 512*1024+1))
	if err != nil || len(data) > 512*1024 {
		return errors.New("Operator response exceeded the limit or was interrupted")
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &failure) == nil && failure.Error != "" {
			return errors.New(Text(failure.Error))
		}
		return fmt.Errorf("Operator request rejected (HTTP %d)", response.StatusCode)
	}
	if err := json.Unmarshal(data, output); err != nil {
		return errors.New("Invalid operator response; verify the daemon version")
	}
	return nil
}

func (c *Client) Snapshot(ctx context.Context) (Snapshot, error) {
	var result Snapshot
	err := c.request(ctx, "GET", "/v1/overview", nil, &result)
	if err == nil && result.Protocol != Protocol {
		err = errors.New("Incompatible operator protocol; relaunch the installed console")
	}
	return result, err
}

func (c *Client) Check(ctx context.Context, component, head string) (Candidate, error) {
	var result Candidate
	err := c.request(ctx, "POST", "/v1/check", map[string]string{"component": component, "head_id": head}, &result)
	return result, err
}

func (c *Client) Act(ctx context.Context, action Action) (Job, error) {
	var result Job
	err := c.request(ctx, "POST", "/v1/actions", action, &result)
	return result, err
}

func (c *Client) Bots(ctx context.Context) ([]Bot, error) {
	var result struct {
		Bots []Bot `json:"bots"`
	}
	err := c.request(ctx, "GET", "/v1/bots", nil, &result)
	return result.Bots, err
}
