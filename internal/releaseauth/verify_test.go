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

func TestVerifyDownloadedBootstrapsAndPinsPublicKey(t *testing.T) {
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
	keyRequests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/owner/repo/releases/download/updater-v1.0.0/updater-release.json.sig.json":
			_, _ = response.Write(envelope)
		case "/owner/repo/releases/download/updater-v1.0.0/updater.pem":
			keyRequests++
			_, _ = response.Write(key)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	signatureURL := server.URL + "/owner/repo/releases/download/updater-v1.0.0/updater-release.json.sig.json"
	if err := VerifyDownloaded(t.Context(), server.Client(), manifestPath, signatureURL, "updater"); err != nil {
		t.Fatal(err)
	}
	pinned, err := os.ReadFile(filepath.Join(trustDirectory, "updater.pem"))
	if err != nil || string(pinned) != string(key) {
		t.Fatal("verified public key was not persisted")
	}
	if err := VerifyDownloaded(t.Context(), server.Client(), manifestPath, signatureURL, "updater"); err != nil {
		t.Fatal(err)
	}
	if keyRequests != 1 {
		t.Fatalf("pinned key should prevent another bootstrap download; got %d requests", keyRequests)
	}
}
