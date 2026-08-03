package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Snapshot struct {
	Schema   string                 `json:"schema"`
	Revision string                 `json:"revision"`
	Checksum string                 `json:"checksum"`
	Values   map[string]interface{} `json:"values"`
}

func Load(kernelURL, token, cachePath string, timeout time.Duration) (Snapshot, error) {
	var cached Snapshot
	cacheValid := false
	if body, err := os.ReadFile(cachePath); err == nil && json.Unmarshal(body, &cached) == nil && verify(cached) == nil {
		cacheValid = true
	}
	parsed, err := url.Parse(kernelURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return Snapshot{}, errors.New("KERNEL_URL must be a valid HTTP or HTTPS URL")
	}
	req, _ := http.NewRequest(http.MethodGet, strings.TrimRight(kernelURL, "/")+"/api/v1/register/snapshot", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.exocortex.register+json; version=1")
	if cacheValid {
		req.Header.Set("If-None-Match", `"`+cached.Revision+`"`)
	}
	client := &http.Client{Timeout: timeout}
	response, remoteErr := client.Do(req)
	if remoteErr == nil {
		defer response.Body.Close()
		if response.StatusCode == http.StatusNotModified && cacheValid {
			return cached, nil
		}
		if response.StatusCode == http.StatusOK {
			body, err := io.ReadAll(io.LimitReader(response.Body, 3*1024*1024+1))
			if err == nil && len(body) <= 3*1024*1024 {
				var snapshot Snapshot
				if json.Unmarshal(body, &snapshot) == nil && verify(snapshot) == nil {
					_ = os.MkdirAll(filepath.Dir(cachePath), 0o700)
					_ = os.WriteFile(cachePath+".tmp", body, 0o600)
					_ = os.Rename(cachePath+".tmp", cachePath)
					return snapshot, nil
				}
			}
		}
		remoteErr = fmt.Errorf("Kernel Register returned HTTP %d", response.StatusCode)
	}
	if cacheValid {
		return cached, nil
	}
	return Snapshot{}, fmt.Errorf("Kernel unavailable and no last-known-good Register exists: %w", remoteErr)
}

func verify(snapshot Snapshot) error {
	if snapshot.Schema != "exocortex.register.snapshot.v1" || snapshot.Revision == "" || snapshot.Values == nil {
		return errors.New("invalid Kernel Register snapshot")
	}
	body, _ := json.Marshal(map[string]interface{}{"values": snapshot.Values})
	sum := sha256.Sum256(body)
	expected := "sha256:" + hex.EncodeToString(sum[:])
	if snapshot.Checksum != expected {
		return errors.New("Kernel Register checksum mismatch")
	}
	return nil
}

func String(snapshot Snapshot, key string) (string, error) {
	var current interface{} = snapshot.Values
	for _, part := range strings.Split(key, ".") {
		object, ok := current.(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("Register key %s is unavailable", key)
		}
		current, ok = object[part]
		if !ok {
			return "", fmt.Errorf("Register key %s is unavailable", key)
		}
	}
	value, ok := current.(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("Register key %s is invalid", key)
	}
	return strings.TrimSpace(value), nil
}
