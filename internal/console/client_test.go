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

func TestScopedRecoveryActionsAreRootOperatorOnlyAndFullySpecified(t *testing.T) {
	updaterCode := map[string]string{"updater": strings.Repeat("u", 32)}
	for _, action := range []Action{
		{Component: "updater", Kind: "recovery-configure", Recovery: &RecoveryInput{GatewayURL: "https://saturn.example", Service: "updater", EnrollmentCodes: updaterCode}},
		{Component: "neptune", Kind: "recovery-export", Recovery: &RecoveryInput{Service: "neptune", Confirmation: "CREATE NEPTUNE RECOVERY"}},
		{Component: "gryphon", Kind: "recovery-restore", Recovery: &RecoveryInput{ArchivePath: "/root/recovery/gryphon.exorecovery", Service: "gryphon", Passphrase: "sixteen-byte-passphrase", Confirmation: "RESTORE GRYPHON"}},
	} {
		if err := ValidateAction(action); err != nil {
			t.Fatalf("valid recovery action rejected: %s: %v", action.Kind, err)
		}
	}
	invalid := Action{Component: "gryphon", Kind: "recovery-restore", HeadID: "saturn", Recovery: &RecoveryInput{ArchivePath: "/tmp/gryphon.exorecovery", Service: "gryphon", Passphrase: "sixteen-byte-passphrase", Confirmation: "RESTORE GRYPHON"}}
	if err := ValidateAction(invalid); err == nil {
		t.Fatal("head-scoped recovery action was accepted")
	}
	invalid = Action{Component: "updater", Kind: "recovery-configure", Recovery: &RecoveryInput{GatewayURL: "http://saturn.example", Service: "updater", EnrollmentCodes: updaterCode}}
	if err := ValidateAction(invalid); err == nil {
		t.Fatal("insecure recovery Gateway was accepted")
	}
	invalid = Action{Component: "updater", Kind: "recovery-export", Recovery: &RecoveryInput{Service: "updater", Passphrase: "sixteen-byte-passphrase", Confirmation: "CREATE"}}
	if err := ValidateAction(invalid); err == nil {
		t.Fatal("recovery export without exact confirmation was accepted")
	}
}
