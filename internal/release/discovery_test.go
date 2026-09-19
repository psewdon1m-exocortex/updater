package release

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type discoveryTransport func(*http.Request) (*http.Response, error)

func (f discoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDiscoveryIgnoresProviderOrderForeignDraftAndPrerelease(t *testing.T) {
	previous := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = previous })
	http.DefaultClient = &http.Client{Transport: discoveryTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.github.com/repos/example/kernel/releases?per_page=100" {
			t.Fatalf("wrong registry repository: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`[
   {"tag_name":"kernel-v1.0.9"}, {"tag_name":"kernel-v99.0.0","draft":true},
   {"tag_name":"kernel-v98.0.0","prerelease":true}, {"tag_name":"kernel-v1.2.0-rc.1"},
   {"tag_name":"volt-v99.0.0"}, {"tag_name":"v99.0.0"}, {"tag_name":"kernel-v01.3.0"},
   {"tag_name":"kernel-v1.0.10"}, {"tag_name":"kernel-v1.0.8"}]`))}, nil
	})}
	result, err := Discover(context.Background(), "https://github.com/example/kernel", "kernel", "1.0.9")
	if err != nil || result.AvailableVersion != "1.0.10" || !result.UpdateAvailable {
		t.Fatalf("%+v %v", result, err)
	}
	for _, current := range []string{"1.0.10", "1.0.11", "2.0.0"} {
		result, err = Discover(context.Background(), "https://github.com/example/kernel", "kernel", current)
		if err != nil || result.UpdateAvailable {
			t.Fatalf("reinstall/downgrade for %s: %+v %v", current, result, err)
		}
	}
}
