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
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type recoveryFaultTransport struct {
	base http.RoundTripper
	mu   sync.Mutex
	lost map[string]bool
}

// Drop a successful response after the real server has accepted the mutation.
func (transport *recoveryFaultTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		return response, err
	}
	transport.mu.Lock()
	key := request.Method + " " + request.URL.Path
	if !transport.lost[key] {
		for pattern := range transport.lost {
			prefix, suffix, wildcard := strings.Cut(pattern, "*")
			if wildcard && strings.HasPrefix(key, prefix) && strings.HasSuffix(key, suffix) {
				key = pattern
				break
			}
		}
	}
	drop := transport.lost[key] && response.StatusCode >= 200 && response.StatusCode < 300
	if drop {
		delete(transport.lost, key)
	}
	transport.mu.Unlock()
	if drop {
		response.Body.Close()
		return nil, io.ErrUnexpectedEOF
	}
	return response, nil
}

func TestPublisherClosesReservationAfterFinalFailure(t *testing.T) {
	for _, stage := range []string{"create", "head", "patch", "complete"} {
		t.Run(stage, func(t *testing.T) {
			var mu sync.Mutex
			deletes, patches := 0, 0
			var stored []byte
			server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if request.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Error("producer credential lost")
				}
				switch {
				case request.Method == http.MethodDelete:
					if request.URL.Path != "/api/v1/backups/updater-host/runs/run-one" {
						t.Error("wrong reservation cancelled")
					}
					deletes++
					stored = nil
					response.WriteHeader(http.StatusNoContent)
				case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/runs"):
					if stage == "create" {
						http.Error(response, "quota", 413)
						return
					}
					_ = json.NewEncoder(response).Encode(map[string]string{"id": "run-one"})
				case request.Method == http.MethodHead:
					if stage == "head" {
						response.WriteHeader(403)
						return
					}
					response.Header().Set("Upload-Offset", fmt.Sprint(len(stored)))
					response.WriteHeader(204)
				case request.Method == http.MethodPatch:
					patches++
					if stage == "patch" {
						http.Error(response, "unsupported media type", 415)
						return
					}
					stored, _ = io.ReadAll(request.Body)
					response.WriteHeader(204)
				case strings.HasSuffix(request.URL.Path, "/complete"):
					http.Error(response, "invalid archive", 400)
				default:
					http.NotFound(response, request)
				}
			}))
			defer server.Close()
			publisher := &gatewayPublisher{base: server.URL, client: server.Client(), chunk: 64}
			_, err := publisher.publish(t.Context(), ScopedArchive{Scope: "updater", Bytes: []byte("encrypted archive")}, "updater-host", "synthetic-token", "component-test", "qa", time.Now())
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				t.Fatal("failed publication reported success")
			}
			want := 1
			if stage == "create" {
				want = 0
			}
			if deletes != want || len(stored) != 0 {
				t.Fatal("failed job left its reservation", deletes, len(stored))
			}
			if stage == "patch" && patches != 1 {
				t.Fatal("permanent 415 was retried", patches)
			}
		})
	}
}

func TestPublisherCancellationCleanupHasIndependentContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	deleted := make(chan struct{}, 1)
	releasePatch := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodDelete:
			deleted <- struct{}{}
			response.WriteHeader(204)
		case http.MethodPost:
			_ = json.NewEncoder(response).Encode(map[string]string{"id": "run-one"})
		case http.MethodHead:
			response.Header().Set("Upload-Offset", "0")
			response.WriteHeader(204)
		case http.MethodPatch:
			cancel()
			<-releasePatch
		}
	}))
	defer func() { close(releasePatch); server.Close() }()
	publisher := &gatewayPublisher{base: server.URL, client: server.Client(), chunk: 64}
	_, err := publisher.publish(ctx, ScopedArchive{Scope: "updater", Bytes: []byte("archive")}, "updater-host", "synthetic-token", "component-test", "qa", time.Now())
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was lost", err)
	}
	select {
	case <-deleted:
	default:
		t.Fatal("cancelled context prevented reservation cleanup")
	}
}

func TestPublisherResumesTransientFailuresAndLostAcknowledgements(t *testing.T) {
	for _, fault := range []string{"create-ack", "patch-ack", "complete-ack", "temporary-502", "temporary-429", "permanent-503"} {
		t.Run(fault, func(t *testing.T) {
			payload := []byte("0123456789abcdef-encrypted-archive")
			digest := sha256.Sum256(payload)
			var mu sync.Mutex
			var stored []byte
			patches, deletes, completions, creations := 0, 0, 0, 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch {
				case request.Method == http.MethodDelete:
					deletes++
					stored = nil
					response.WriteHeader(204)
				case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/runs"):
					creations++
					if request.Header.Get("Idempotency-Key") != "component-test-updater" {
						t.Error("retry lost idempotency key")
					}
					_ = json.NewEncoder(response).Encode(map[string]string{"id": "run-one"})
				case request.Method == http.MethodHead:
					response.Header().Set("Upload-Offset", fmt.Sprint(len(stored)))
					response.WriteHeader(204)
				case request.Method == http.MethodPatch:
					patches++
					if fault == "permanent-503" {
						http.Error(response, "unavailable", 503)
						return
					}
					if patches == 1 && fault == "temporary-502" {
						http.Error(response, "bad gateway", 502)
						return
					}
					if patches == 1 && fault == "temporary-429" {
						http.Error(response, "busy", 429)
						return
					}
					if request.Header.Get("Content-Type") != "application/offset+octet-stream" || request.Header.Get("Upload-Offset") != fmt.Sprint(len(stored)) {
						t.Error("retry duplicated bytes or lost binary media type")
					}
					chunk, _ := io.ReadAll(request.Body)
					stored = append(stored, chunk...)
					response.WriteHeader(204)
				case strings.HasSuffix(request.URL.Path, "/complete"):
					completions++
					_ = json.NewEncoder(response).Encode(map[string]any{"id": "run-one", "receipt": map[string]any{
						"logicalPath": "/backups/updater/archive.exorecovery", "sha256": hex.EncodeToString(digest[:]), "sizeBytes": len(payload)}})
				}
			}))
			defer server.Close()
			lost := map[string]bool{}
			switch fault {
			case "create-ack":
				lost["POST /api/v1/backups/updater-host/runs"] = true
			case "patch-ack":
				lost["PATCH /api/v1/backups/updater-host/runs/run-one/upload"] = true
			case "complete-ack":
				lost["POST /api/v1/backups/updater-host/runs/run-one/complete"] = true
			}
			client := server.Client()
			client.Transport = &recoveryFaultTransport{base: client.Transport, lost: lost}
			publisher := &gatewayPublisher{base: server.URL, client: client, chunk: 8}
			_, err := publisher.publish(t.Context(), ScopedArchive{Scope: "updater", Bytes: payload}, "updater-host", "synthetic-token", "component-test", "qa", time.Now())
			mu.Lock()
			defer mu.Unlock()
			if fault == "permanent-503" {
				if err == nil || patches != 3 || deletes != 1 {
					t.Fatal("retry limit or terminal cleanup failed", err, patches, deletes)
				}
				return
			}
			if err != nil || deletes != 0 || !bytes.Equal(stored, payload) {
				t.Fatal("recoverable failure became a failed job", err, deletes, string(stored))
			}
			if fault == "create-ack" && creations != 2 {
				t.Fatal("reservation creation was not replayed")
			}
			if fault == "complete-ack" && completions != 2 {
				t.Fatal("completion was not replayed")
			}
			if fault == "patch-ack" && patches != (len(payload)+7)/8 {
				t.Fatal("accepted chunk was sent twice", patches)
			}
		})
	}
}
