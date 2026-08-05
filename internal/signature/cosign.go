package signature

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// VerifyBlob runs Cosign with a writable, operation-local home directory.
// The updater systemd sandbox protects /root, while Cosign needs a home for
// its Sigstore TUF and Rekor trust cache.
func VerifyBlob(ctx context.Context, artifact, bundle, home string) error {
	if _, err := exec.LookPath("cosign"); err != nil {
		return errors.New("cosign is required to verify production releases")
	}
	home, err := filepath.Abs(home)
	if err != nil {
		return fmt.Errorf("resolve Sigstore home: %w", err)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("create Sigstore home: %w", err)
	}
	command := exec.CommandContext(ctx, "cosign", "verify-blob",
		"--bundle", bundle,
		"--certificate-identity-regexp", `^https://github.com/.+/.github/workflows/release.yml@refs/tags/.+$`,
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com",
		artifact,
	)
	command.Env = environmentWithValue(os.Environ(), "HOME", home)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(output)))
	}
	return nil
}

func environmentWithValue(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if !strings.HasPrefix(item, prefix) {
			result = append(result, item)
		}
	}
	return append(result, prefix+value)
}
