package releaseauth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNodeProducerSignatureAndTamperRejection(t *testing.T) {
	read := func(name string) []byte {
		value, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	manifest, envelope, key := read("manifest.json"), read("manifest.json.sig.json"), read("public.pem")
	if err := VerifyBytes(manifest, envelope, key); err != nil {
		t.Fatal(err)
	}
	manifest[10] ^= 1
	if VerifyBytes(manifest, envelope, key) == nil {
		t.Fatal("tampered manifest accepted")
	}
	if VerifyBytes(manifest, nil, key) == nil {
		t.Fatal("unsigned manifest accepted")
	}
	if VerifyBytes(manifest, envelope, []byte("untrusted")) == nil {
		t.Fatal("missing trust accepted")
	}
}

func TestVerifyDownloadedRequiresPreviouslyPinnedPublicKey(t *testing.T) {
	read := func(name string) []byte {
		value, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	manifest := read("manifest.json")
	envelope := read("manifest.json.sig.json")
	key := read("public.pem")
	directory := t.TempDir()
	manifestPath := filepath.Join(directory, "manifest.json")
	if err := os.WriteFile(manifestPath, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	trustDirectory := filepath.Join(directory, "trust")
	t.Setenv("EXOCORTEX_RELEASE_TRUST_DIR", trustDirectory)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/owner/repo/releases/download/updater-v1.0.0/updater-release.json.sig.json":
			_, _ = response.Write(envelope)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	signatureURL := server.URL + "/owner/repo/releases/download/updater-v1.0.0/updater-release.json.sig.json"
	if err := VerifyDownloaded(t.Context(), server.Client(), manifestPath, signatureURL, "updater"); err == nil {
		t.Fatal("missing pinned release trust was accepted")
	}
	if err := os.MkdirAll(trustDirectory, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trustDirectory, "updater.pem"), key, 0644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyDownloaded(t.Context(), server.Client(), manifestPath, signatureURL, "updater"); err != nil {
		t.Fatal(err)
	}
}
