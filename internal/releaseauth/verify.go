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
	"path"
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

func releasePublicKeyURL(signatureURL, service string) (string, error) {
	parsed, err := url.Parse(signatureURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return "", errors.New("signed release manifest is required")
	}
	parsed.Path = path.Join(path.Dir(parsed.Path), service+".pem")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func persistBootstrappedKey(directory, service string, value []byte) error {
	if err := os.MkdirAll(directory, 0755); err != nil {
		return errors.New("release trust directory cannot be created")
	}
	target := filepath.Join(directory, service+".pem")
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := os.ReadFile(target)
		if readErr != nil || string(existing) != string(value) {
			return errors.New("release trust key changed during bootstrap")
		}
		return nil
	}
	if err != nil {
		return errors.New("release trust key cannot be installed")
	}
	if err = file.Chmod(0644); err != nil {
		_ = file.Close()
		_ = os.Remove(target)
		return errors.New("release trust key cannot be installed")
	}
	if _, err = file.Write(value); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(target)
		return errors.New("release trust key cannot be installed")
	}
	return nil
}

// An existing host key stays pinned. On first use the public key is bootstrapped
// from the same HTTPS GitHub release as the manifest, verified, then persisted.
func VerifyDownloaded(ctx context.Context, client *http.Client, manifestPath, signatureURL, service string) error {
	switch service {
	case "kernel", "volt", "saturn", "updater", "neptune", "gryphon":
	default:
		return errors.New("unsupported release trust scope")
	}
	publicKeyURL, err := releasePublicKeyURL(signatureURL, service)
	if err != nil {
		return errors.New("signed release manifest is required")
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
	bootstrap := false
	if errors.Is(err, os.ErrNotExist) {
		key, err = downloadLimited(ctx, client, publicKeyURL, 16384, "release public key is unavailable")
		bootstrap = true
	}
	if err != nil {
		return errors.New("release trust key is unavailable for " + service)
	}
	if err = VerifyBytes(manifest, envelope, key); err != nil {
		return err
	}
	if bootstrap {
		return persistBootstrappedKey(trustDirectory, service, key)
	}
	return nil
}
