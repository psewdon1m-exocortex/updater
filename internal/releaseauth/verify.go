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
	"net/url"
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

// Trust comes from a host-provisioned key, never from the downloaded release.
func VerifyDownloaded(ctx context.Context, client *http.Client, manifestPath, signatureURL, service string) error {
	switch service {
	case "kernel", "volt", "saturn", "updater", "neptune", "gryphon":
	default:
		return errors.New("unsupported release trust scope")
	}
	parsed, err := url.Parse(signatureURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return errors.New("signed release manifest is required")
	}
	trustDirectory := os.Getenv("EXOCORTEX_RELEASE_TRUST_DIR")
	if trustDirectory == "" {
		trustDirectory = "/etc/exocortex/release-trust"
	}
	key, err := os.ReadFile(filepath.Join(trustDirectory, service+".pem"))
	if err != nil {
		return errors.New("release trust key is not provisioned for " + service)
	}
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, signatureURL, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("release signature is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("release signature is unavailable")
	}
	envelope, err := io.ReadAll(io.LimitReader(response.Body, 16385))
	if err != nil {
		return errors.New("release signature cannot be read")
	}
	return VerifyBytes(manifest, envelope, key)
}
