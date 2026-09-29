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

func TestDiscoveryRetriesTransientProviderFailure(t *testing.T) {
	previous := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = previous })
	calls := 0
	http.DefaultClient = &http.Client{Transport: discoveryTransport(func(_ *http.Request) (*http.Response, error) {
		calls++
		if calls < 3 {
			return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`[{"tag_name":"updater-v0.6.5"}]`))}, nil
	})}
	result, err := Discover(context.Background(), "https://github.com/example/updater", "updater", "0.6.5")
	if err != nil || calls != 3 || result.AvailableVersion != "0.6.5" || result.UpdateAvailable {
		t.Fatalf("transient registry failure was not recovered: calls=%d candidate=%+v err=%v", calls, result, err)
	}
}

func TestDiscoveryDoesNotRetryRateLimit(t *testing.T) {
	previous := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = previous })
	calls := 0
	http.DefaultClient = &http.Client{Transport: discoveryTransport(func(_ *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{"X-Ratelimit-Remaining": []string{"0"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	_, err := Discover(context.Background(), "https://github.com/example/updater", "updater", "0.6.5")
	if calls != 1 || err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("rate limit must remain visible without a retry: calls=%d err=%v", calls, err)
	}
}
