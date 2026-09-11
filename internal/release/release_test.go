package release

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSupportsMinimum(t *testing.T) {
	cases := []struct {
		current string
		minimum string
		want    bool
	}{
		{"1.2.0", "1.1.0", true},
		{"1.2.0", "1.2.0", true},
		{"1.1.9", "1.2.0", false},
		{"1.2.0-beta.1", "1.2.0", false},
		{"invalid", "1.2.0", false},
	}
	for _, item := range cases {
		if got := SupportsMinimum(item.current, item.minimum); got != item.want {
			t.Fatalf("SupportsMinimum(%q, %q) = %v, want %v", item.current, item.minimum, got, item.want)
		}
	}
}

func TestVersionFromServiceTagUsesModuleScopedTags(t *testing.T) {
	tests := []struct {
		service string
		tag     string
		version string
		ok      bool
	}{
		{service: "saturn", tag: "saturn-v0.1.0", version: "0.1.0", ok: true},
		{service: "kernel", tag: "kernel-v2.3.4", version: "2.3.4", ok: true},
		{service: "saturn", tag: "SATURN-v0.1.0", version: "0.1.0", ok: true},
		{service: "saturn", tag: "v0.1.0", ok: false},
		{service: "saturn", tag: "saturn-v0.1.0-beta.1", version: "0.1.0-beta.1", ok: true},
		{service: "saturn", tag: "saturn-v01.0.0", ok: false},
	}
	for _, item := range tests {
		version, ok := versionFromServiceTag(item.service, item.tag)
		if version != item.version || ok != item.ok {
			t.Fatalf("versionFromServiceTag(%q, %q) = (%q, %v), want (%q, %v)", item.service, item.tag, version, ok, item.version, item.ok)
		}
	}
}

func TestDownloadEnforcesConfiguredLimit(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "11")
		_, _ = io.WriteString(w, "hello world")
	}))
	defer server.Close()

	target := filepath.Join(t.TempDir(), "head-compose.tar.gz")
	err := download(context.Background(), server.Client(), server.URL, target, 10)
	if err == nil || !strings.Contains(err.Error(), `release asset "head-compose.tar.gz"`) {
		t.Fatalf("download() error = %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("oversized asset should not be created, stat error = %v", statErr)
	}
}

func TestComposeBundleLimitCoversPublishedHeadBundles(t *testing.T) {
	const publishedBundleBytes = 63_597_418
	if maxComposeBundleBytes < publishedBundleBytes {
		t.Fatalf("compose bundle limit %d is below published bundle size %d", maxComposeBundleBytes, publishedBundleBytes)
	}
}
