package kernel

import (
	"bytes"
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
	"regexp"
	"sort"
	"strings"
	"time"
)

var voltReference = regexp.MustCompile(`(?i)^volt://[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}/[1-5]$`)

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
			return resolveSnapshot(kernelURL, token, cached, client)
		}
		if response.StatusCode == http.StatusOK {
			body, err := io.ReadAll(io.LimitReader(response.Body, 3*1024*1024+1))
			if err == nil && len(body) <= 3*1024*1024 {
				var snapshot Snapshot
				if json.Unmarshal(body, &snapshot) == nil && verify(snapshot) == nil {
					_ = os.MkdirAll(filepath.Dir(cachePath), 0o700)
					_ = os.WriteFile(cachePath+".tmp", body, 0o600)
					_ = os.Rename(cachePath+".tmp", cachePath)
					return resolveSnapshot(kernelURL, token, snapshot, client)
				}
			}
		}
		remoteErr = fmt.Errorf("Kernel Register returned HTTP %d", response.StatusCode)
	}
	if cacheValid {
		return resolveSnapshot(kernelURL, token, cached, client)
	}
	return Snapshot{}, fmt.Errorf("Kernel unavailable and no last-known-good Register exists: %w", remoteErr)
}

type resolutionResponse struct {
	Schema string `json:"schema"`
	Values map[string]struct {
		Value string `json:"value"`
	} `json:"values"`
}

func collectReferences(value interface{}, prefix string, output map[string]string) error {
	switch current := value.(type) {
	case map[string]interface{}:
		for key, child := range current {
			next := key
			if prefix != "" {
				next = prefix + "." + key
			}
			if err := collectReferences(child, next, output); err != nil {
				return err
			}
		}
	case string:
		if !voltReference.MatchString(current) {
			return fmt.Errorf("Register key %s is not mapped to Volt", prefix)
		}
		output[prefix] = current
	default:
		return fmt.Errorf("Register key %s is not a string reference", prefix)
	}
	return nil
}

func setDotted(values map[string]interface{}, key, value string) {
	parts := strings.Split(key, ".")
	current := values
	for _, part := range parts[:len(parts)-1] {
		current = current[part].(map[string]interface{})
	}
	current[parts[len(parts)-1]] = value
}

func resolveSnapshot(kernelURL, token string, snapshot Snapshot, client *http.Client) (Snapshot, error) {
	references := map[string]string{}
	if err := collectReferences(snapshot.Values, "", references); err != nil {
		return Snapshot{}, err
	}
	keys := make([]string, 0, len(references))
	for key := range references {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for start := 0; start < len(keys); start += 20 {
		end := start + 20
		if end > len(keys) {
			end = len(keys)
		}
		body, _ := json.Marshal(map[string]interface{}{"keys": keys[start:end]})
		req, _ := http.NewRequest(http.MethodPost, strings.TrimRight(kernelURL, "/")+"/api/v1/register/resolve", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			return Snapshot{}, fmt.Errorf("Kernel value resolution failed: %w", err)
		}
		payload, readErr := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
		response.Body.Close()
		if readErr != nil || len(payload) > 1024*1024 || response.StatusCode != http.StatusOK {
			return Snapshot{}, fmt.Errorf("Kernel value resolution returned HTTP %d", response.StatusCode)
		}
		var resolved resolutionResponse
		if json.Unmarshal(payload, &resolved) != nil || resolved.Schema != "exocortex.register.resolution.v1" {
			return Snapshot{}, errors.New("invalid Kernel value resolution response")
		}
		for _, key := range keys[start:end] {
			item, ok := resolved.Values[key]
			if !ok {
				return Snapshot{}, fmt.Errorf("Kernel omitted Register key %s", key)
			}
			setDotted(snapshot.Values, key, item.Value)
		}
	}
	return snapshot, nil
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
