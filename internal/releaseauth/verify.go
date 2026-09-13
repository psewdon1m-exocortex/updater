package releaseauth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

type signature struct {
	Schema    string `json:"schema"`
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	Signature string `json:"signature"`
}

func VerifyBytes(manifest, envelope, publicPEM []byte) error {
	if len(manifest) > 2*1024*1024 || len(envelope) > 16384 || len(publicPEM) > 16384 {
		return errors.New("release signature input exceeds limit")
	}
	var signed signature
	if json.Unmarshal(envelope, &signed) != nil || signed.Schema != "exocortex.release-signature.v1" || signed.Algorithm != "RSA-PSS-SHA256" {
		return errors.New("release signature envelope is invalid")
	}
	block, rest := pem.Decode(publicPEM)
	if block == nil || block.Type != "PUBLIC KEY" || len(rest) != 0 {
		return errors.New("release trust key must be a single SPKI public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return errors.New("release trust key is invalid")
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok || key.N.BitLen() < 3072 {
		return errors.New("release trust key must be RSA with at least 3072 bits")
	}
	id := sha256.Sum256(block.Bytes)
	if signed.KeyID != hex.EncodeToString(id[:]) {
		return errors.New("release signer is not trusted")
	}
	value, err := base64.StdEncoding.Strict().DecodeString(signed.Signature)
	if err != nil {
		return errors.New("release signature is invalid")
	}
	hash := sha256.Sum256(manifest)
	if rsa.VerifyPSS(key, crypto.SHA256, hash[:], value, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}) != nil {
		return errors.New("release signature verification failed")
	}
	return nil
}

func downloadLimited(ctx context.Context, client *http.Client, location string, limit int64, unavailable string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New(unavailable)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New(unavailable)
	}
	value, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(value)) > limit {
		return nil, errors.New(unavailable)
	}
	return value, nil
}

// Release trust must already have been pinned by an authenticated bootstrap.
// Head bootstraps carry the signed Updater installer, which also provisions
// helper trust. A public key beside a manifest is not an authentication anchor.
func VerifyDownloaded(ctx context.Context, client *http.Client, manifestPath, signatureURL, service string) error {
	switch service {
	case "kernel", "volt", "saturn", "updater", "neptune", "gryphon":
	default:
		return errors.New("unsupported release trust scope")
	}
	trustDirectory := os.Getenv("EXOCORTEX_RELEASE_TRUST_DIR")
	if trustDirectory == "" {
		trustDirectory = "/etc/exocortex/release-trust"
	}
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	envelope, err := downloadLimited(ctx, client, signatureURL, 16384, "release signature is unavailable")
	if err != nil {
		return err
	}
	keyPath := filepath.Join(trustDirectory, service+".pem")
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return errors.New("release trust key is unavailable for " + service + "; run an exact-version Updater or head bootstrap first")
	}
	return VerifyBytes(manifest, envelope, key)
}
