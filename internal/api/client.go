package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"time"
)

func Client(socketPath string) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 2 * time.Second}
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}
}

func Request(socketPath, method, path string, payload interface{}, target interface{}) error {
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			return err
		}
	}
	request, err := http.NewRequest(method, "http://updater.local"+path, &body)
	if err != nil {
		return err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := Client(socketPath).Do(request)
	if err != nil {
		return err
	}
	return DecodeResponse(response, target)
}
