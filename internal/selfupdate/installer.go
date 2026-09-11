package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"updater/internal/config"
)

func extractInstallation(archive, destination, binaryHash string) (string, error) {
	file, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return "", err
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	seen := map[string]bool{}
	expected := map[string]int64{"updater/updater-linux-amd64": 64 * 1024 * 1024, "updater/install.sh": 256 * 1024, "updater/systemd/updater.service": 64 * 1024}
	for {
		h, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if h.Typeflag == tar.TypeDir && (h.Name == "updater/" || h.Name == "updater/systemd/") {
			continue
		}
		limit, ok := expected[h.Name]
		if !ok || seen[h.Name] || h.Typeflag != tar.TypeReg || h.Size < 1 || h.Size > limit {
			return "", errors.New("invalid signed installer archive member")
		}
		seen[h.Name] = true
		target := filepath.Join(destination, filepath.FromSlash(h.Name))
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return "", err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if err != nil {
			return "", err
		}
		_, err = io.CopyN(output, reader, h.Size)
		if err == nil {
			err = output.Sync()
		}
		closeErr := output.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	if len(seen) != len(expected) {
		return "", errors.New("signed installer archive is incomplete")
	}
	root := filepath.Join(destination, "updater")
	if err = verify(filepath.Join(root, "updater-linux-amd64"), binaryHash); err != nil {
		return "", err
	}
	return root, nil
}

// Only an authenticated release installer may change these host-level settings.
// A supervisor outside updater.service's write sandbox executes the typed mode.
func prepareHost(ctx context.Context, root string, head config.HeadConfig) (func() error, error) {
	const unit = "/etc/systemd/system/updater.service"
	previous, err := os.ReadFile(unit)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	rollback := func() error {
		var err error
		if existed {
			err = os.WriteFile(unit, previous, 0644)
		} else {
			err = os.Remove(unit)
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		}
		return errors.Join(err, exec.Command("systemctl", "daemon-reload").Run())
	}
	command := exec.CommandContext(ctx, "sh", filepath.Join(root, "install.sh"), head.ID, head.EnvFile, filepath.Join(root, "updater-linux-amd64"))
	command.Env = append(os.Environ(), "UPDATER_PREPARE_ONLY=true")
	if err = command.Run(); err != nil {
		return nil, errors.Join(errors.New("signed Updater host preparation failed"), rollback())
	}
	return rollback, nil
}
