package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixtureClient(code int, body string) *Client {
	return &Client{http: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		recorder.WriteHeader(code)
		_, _ = recorder.WriteString(body)
		return recorder.Result(), nil
	})}}
}

func TestClientRejectsOversizeInvalidAndWrongProtocol(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", 512*1024+1), `{"protocol":999}`, `not json`} {
		if _, err := fixtureClient(200, body).Snapshot(context.Background()); err == nil {
			t.Fatal("accepted invalid response")
		}
	}
}

func TestTextRemovesOSCAndControlSequences(t *testing.T) {
	value := Text("a\x1b]52;c;c2VjcmV0\a\x1b[2Jb\u202e\r\n")
	if value != "ab" {
		t.Fatalf("unsafe text: %q", value)
	}
	if len([]rune(Text(strings.Repeat("x", 1000)))) > 303 {
		t.Fatal("unbounded text")
	}
}

func TestEnrollmentUsesExistingCodeAlphabetAndLoopbackContract(t *testing.T) {
	action := Action{Component: "neptune", Kind: "enroll", SetupCode: strings.Repeat("a", 30) + "_-", ExportURL: "http://127.0.0.1:18180/api/internal/neptune/backup"}
	if err := ValidateAction(action); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{"http://external.invalid/backup", "http://user:secret@127.0.0.1/backup", "http://127.0.0.1/backup?token=secret", "https://127.0.0.1/backup", "http://127.0.0.1/backup#fragment"} {
		action.ExportURL = url
		if err := ValidateAction(action); err == nil {
			t.Fatalf("accepted unsafe export URL %s", url)
		}
	}
}
