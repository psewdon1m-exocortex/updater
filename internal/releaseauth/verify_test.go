package releaseauth

import (
	"os"
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
