package component

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type wyvernReleaseTransport func(*http.Request) (*http.Response, error)

func (f wyvernReleaseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestEmbeddedWyvernInstallerCannotInstallPrerelease(t *testing.T) {
	for _, kind := range []string{"qualified", "prerelease", "draft", "wrong-tag", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			body := []byte(`{"schema":"exocortex.wyvern.release.v1","product":"wyvern","version":"0.0.1","image":"ghcr.io/example/wyvern@sha256:` + strings.Repeat("a", 64) + `","api_version":1,"config_schema":"exocortex.wyvern.config.v1","capabilities":["text","streaming","structured_output","token_count"],"installer":{"url":"https://github.com/example/wyvern/releases/download/wyvern-v0.0.1/wyvern-install.tar.gz","sha256":"` + strings.Repeat("b", 64) + `"}}`)
			manifest, err := ParseWyvernManifest(body)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: wyvernReleaseTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != "https://api.github.com/repos/example/wyvern/releases/tags/wyvern-v0.0.1" {
					t.Fatalf("unexpected release authority: %s", r.URL)
				}
				tag := "wyvern-v0.0.1"
				if kind == "wrong-tag" {
					tag = "wyvern-v0.0.2"
				}
				result, _ := json.Marshal(map[string]any{"tag_name": tag, "draft": kind == "draft", "prerelease": kind == "prerelease"})
				status := 200
				if kind == "unavailable" {
					status = 404
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(result))), Header: make(http.Header)}, nil
			})}
			err = verifyPublishedWyvern(context.Background(), client, manifest)
			if (err == nil) != (kind == "qualified") {
				t.Fatalf("publication gate outcome for %s: %v", kind, err)
			}
		})
	}
}
