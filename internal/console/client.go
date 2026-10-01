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
	"sync"
	"time"
)

type Client struct {
	http        *http.Client
	mu          sync.Mutex
	windowLease string
	windowStop  context.CancelFunc
}

func NewClient(socket string) *Client {
	return &Client{http: &http.Client{
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
		}},
		Timeout:       50 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *Client) Close() {
	c.mu.Lock()
	lease, stop := c.windowLease, c.windowStop
	c.windowLease, c.windowStop = "", nil
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
	if len(lease) == 48 {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var response struct {
			Open bool `json:"open"`
		}
		_ = c.request(ctx, "POST", "/v1/window/close", map[string]string{"lease_id": lease}, &response)
	}
	c.http.CloseIdleConnections()
}

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
	if err == nil && action.Component == "window" {
		if action.Kind == "open" && len(result.WindowLeaseID) == 48 {
			c.mu.Lock()
			if c.windowStop != nil {
				c.windowStop()
			}
			heartbeatContext, stop := context.WithCancel(context.Background())
			c.windowStop, c.windowLease = stop, result.WindowLeaseID
			c.mu.Unlock()
			go c.keepWindowOpen(heartbeatContext, result.WindowLeaseID)
		}
		if action.Kind == "revoke" || action.Kind == "pair" {
			c.mu.Lock()
			if c.windowStop != nil {
				c.windowStop()
			}
			c.windowStop, c.windowLease = nil, ""
			c.mu.Unlock()
		}
	}
	return result, err
}

func (c *Client) keepWindowOpen(ctx context.Context, lease string) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			beatContext, cancel := context.WithTimeout(ctx, 3*time.Second)
			var response struct {
				Open bool `json:"open"`
			}
			err := c.request(beatContext, "POST", "/v1/window/heartbeat", map[string]string{"lease_id": lease}, &response)
			cancel()
			if err != nil || !response.Open {
				return
			}
		}
	}
}

func (c *Client) Bots(ctx context.Context) ([]Bot, error) {
	var result struct {
		Bots []Bot `json:"bots"`
	}
	err := c.request(ctx, "GET", "/v1/bots", nil, &result)
	return result.Bots, err
}
