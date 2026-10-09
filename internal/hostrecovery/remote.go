package hostrecovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"updater/internal/config"
)

type PublishedArchive struct {
	Scope       string
	LogicalPath string
	SHA256      string
	Size        int
}

type backupCapabilities struct {
	Schema        string `json:"schema"`
	MaxChunkBytes int    `json:"maxChunkBytes"`
}

type backupRun struct {
	ID      string `json:"id"`
	Receipt *struct {
		LogicalPath string `json:"logicalPath"`
		SHA256      string `json:"sha256"`
		SizeBytes   int    `json:"sizeBytes"`
	} `json:"receipt,omitempty"`
}

type enrollmentResult struct {
	Token         string `json:"token"`
	Slug          string `json:"slug"`
	NamespaceSlug string `json:"namespaceSlug"`
}

type gatewayPublisher struct {
	base   string
	client *http.Client
	chunk  int
}

func newGatewayPublisher(ctx context.Context, raw string) (*gatewayPublisher, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("recovery Gateway URL is invalid")
	}
	client := &http.Client{Timeout: 6 * time.Minute, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	publisher := &gatewayPublisher{base: strings.TrimRight(raw, "/"), client: client}
	var capabilities backupCapabilities
	if err := publisher.json(ctx, http.MethodGet, "/api/v1/backups/capabilities", "", "", nil, &capabilities); err != nil {
		return nil, fmt.Errorf("read Saturn backup capabilities: %w", err)
	}
	if capabilities.Schema != "saturn.backup-ingest.capabilities.v1" || capabilities.MaxChunkBytes < 1 || capabilities.MaxChunkBytes > 64*1024*1024 {
		return nil, errors.New("Saturn backup capabilities are incompatible")
	}
	publisher.chunk = capabilities.MaxChunkBytes
	return publisher, nil
}

func (publisher *gatewayPublisher) request(ctx context.Context, method, path, token, idempotency string, body io.Reader, contentLength int64) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, publisher.base+path, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if idempotency != "" {
		request.Header.Set("Idempotency-Key", idempotency)
	}
	if body != nil {
		request.ContentLength = contentLength
		request.Header.Set("Content-Type", "application/json")
	}
	return publisher.client.Do(request)
}

func responseError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return fmt.Errorf("Gateway request failed with status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
}

func (publisher *gatewayPublisher) json(ctx context.Context, method, path, token, idempotency string, input any, output any) error {
	var body []byte
	var reader io.Reader
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(body)
	}
	response, err := publisher.request(ctx, method, path, token, idempotency, reader, int64(len(body)))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError(response)
	}
	if output == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return err
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1024*1024))
	return decoder.Decode(output)
}

func (publisher *gatewayPublisher) uploadOffset(ctx context.Context, path, token string, expected int) (int, error) {
	response, err := publisher.request(ctx, http.MethodHead, path, token, "", nil, 0)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, responseError(response)
	}
	var offset int
	if _, err := fmt.Sscan(response.Header.Get("Upload-Offset"), &offset); err != nil || offset < 0 || offset > expected {
		return 0, errors.New("Gateway returned an invalid backup upload offset")
	}
	return offset, nil
}

func enrollScopes(ctx context.Context, raw string, client *http.Client, codes map[string]string) (map[string]config.RecoveryIdentity, error) {
	base := strings.TrimRight(raw, "/")
	identities := map[string]config.RecoveryIdentity{}
	if len(codes) == 0 {
		return nil, errors.New("at least one recovery setup code is required")
	}
	for _, scope := range RecoveryScopes {
		code, selected := codes[scope]
		if !selected {
			continue
		}
		body, err := json.Marshal(map[string]string{"code": code})
		if err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/backup-enrollments/redeem", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("redeem %s recovery setup code: %w", scope, err)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			err = responseError(response)
			response.Body.Close()
			return nil, fmt.Errorf("redeem %s recovery setup code: %w", scope, err)
		}
		var result enrollmentResult
		err = json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&result)
		response.Body.Close()
		if err != nil || result.NamespaceSlug != scope || !regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`).MatchString(result.Slug) || !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(result.Token) {
			return nil, fmt.Errorf("Saturn returned an invalid %s recovery identity", scope)
		}
		identities[scope] = config.RecoveryIdentity{Slug: result.Slug, Token: result.Token}
	}
	if len(identities) != len(codes) {
		return nil, errors.New("unknown recovery service setup code")
	}
	return identities, nil
}

func EnrollScopes(ctx context.Context, raw string, codes map[string]string) (map[string]config.RecoveryIdentity, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("recovery Gateway URL is invalid")
	}
	client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return enrollScopes(ctx, raw, client, codes)
}

func (publisher *gatewayPublisher) publish(ctx context.Context, archive ScopedArchive, slug, token, setID, version string, createdAt time.Time) (PublishedArchive, error) {
	digest := sha256.Sum256(archive.Bytes)
	checksum := hex.EncodeToString(digest[:])
	filename := ScopeFilename(archive.Scope, setID)
	payload := map[string]any{
		"filename": filename, "createdAt": createdAt.UTC().Format(time.RFC3339Nano), "backupType": "helper_recovery",
		"expectedSize": len(archive.Bytes), "sha256": checksum, "sourceVersion": version, "encrypted": true,
	}
	var run backupRun
	path := "/api/v1/backups/" + slug + "/runs"
	if err := publisher.json(ctx, http.MethodPost, path, token, setID+"-"+archive.Scope, payload, &run); err != nil {
		return PublishedArchive{}, err
	}
	if run.ID == "" {
		return PublishedArchive{}, errors.New("Gateway returned an invalid backup run")
	}
	uploadPath := path + "/" + run.ID + "/upload"
	offset, err := publisher.uploadOffset(ctx, uploadPath, token, len(archive.Bytes))
	if err != nil {
		return PublishedArchive{}, err
	}
	for offset < len(archive.Bytes) {
		length := min(publisher.chunk, len(archive.Bytes)-offset)
		request, err := http.NewRequestWithContext(ctx, http.MethodPatch, publisher.base+uploadPath, bytes.NewReader(archive.Bytes[offset:offset+length]))
		if err != nil {
			return PublishedArchive{}, err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Upload-Offset", fmt.Sprint(offset))
		request.ContentLength = int64(length)
		response, err := publisher.client.Do(request)
		if err != nil {
			return PublishedArchive{}, err
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			err = responseError(response)
			response.Body.Close()
			return PublishedArchive{}, err
		}
		response.Body.Close()
		offset += length
	}
	var completed backupRun
	if err := publisher.json(ctx, http.MethodPost, path+"/"+run.ID+"/complete", token, "", map[string]any{}, &completed); err != nil {
		return PublishedArchive{}, err
	}
	if completed.Receipt == nil || completed.Receipt.SHA256 != checksum || completed.Receipt.SizeBytes != len(archive.Bytes) || !strings.HasPrefix(completed.Receipt.LogicalPath, "/backups/"+archive.Scope+"/") {
		return PublishedArchive{}, errors.New("Gateway backup receipt does not match the recovery archive")
	}
	return PublishedArchive{Scope: archive.Scope, LogicalPath: completed.Receipt.LogicalPath, SHA256: checksum, Size: len(archive.Bytes)}, nil
}

func PublishScopes(ctx context.Context, runtime config.Runtime, archives []ScopedArchive, setID string, createdAt time.Time) ([]PublishedArchive, error) {
	configuration, err := config.LoadHost(runtime)
	if err != nil {
		return nil, err
	}
	if configuration.RecoveryGatewayURL == "" {
		return nil, errors.New("recovery Gateway is not configured; use updater tui")
	}
	publisher, err := newGatewayPublisher(ctx, configuration.RecoveryGatewayURL)
	if err != nil {
		return nil, err
	}
	result := []PublishedArchive{}
	for _, scope := range RecoveryScopes {
		var selected *ScopedArchive
		for index := range archives {
			if archives[index].Scope == scope {
				selected = &archives[index]
				break
			}
		}
		if selected == nil {
			return nil, fmt.Errorf("recovery set is missing %s", scope)
		}
		token, err := config.RecoveryToken(configuration, scope)
		if err != nil {
			return nil, err
		}
		slug := configuration.RecoverySlugs[scope]
		if slug == "" {
			return nil, fmt.Errorf("%s recovery producer slug is not configured", scope)
		}
		published, err := publisher.publish(ctx, *selected, slug, token, setID, runtime.UpdaterVersion, createdAt)
		if err != nil {
			return nil, fmt.Errorf("publish %s recovery: %w", scope, err)
		}
		result = append(result, published)
	}
	return result, nil
}

// PublishScope publishes one independently configured recovery archive. Other
// host services do not need to be installed or enrolled.
func PublishScope(ctx context.Context, runtime config.Runtime, archive ScopedArchive, setID string, createdAt time.Time) (PublishedArchive, error) {
	if !ValidScope(archive.Scope) {
		return PublishedArchive{}, errors.New("unknown helper recovery scope")
	}
	configuration, err := config.LoadHost(runtime)
	if err != nil {
		return PublishedArchive{}, err
	}
	if configuration.RecoveryGatewayURL == "" {
		return PublishedArchive{}, errors.New("recovery Gateway is not configured; use updater tui")
	}
	publisher, err := newGatewayPublisher(ctx, configuration.RecoveryGatewayURL)
	if err != nil {
		return PublishedArchive{}, err
	}
	token, err := config.RecoveryToken(configuration, archive.Scope)
	if err != nil {
		return PublishedArchive{}, err
	}
	slug := configuration.RecoverySlugs[archive.Scope]
	if slug == "" {
		return PublishedArchive{}, fmt.Errorf("%s recovery producer slug is not configured", archive.Scope)
	}
	return publisher.publish(ctx, archive, slug, token, setID, runtime.UpdaterVersion, createdAt)
}
