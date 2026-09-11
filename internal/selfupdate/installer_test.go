package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestSignedInstallerExtractionRejectsTraversalLinksAndBinaryMismatch(t *testing.T) {
	binary := []byte("synthetic signed executable")
	hash := fmt.Sprintf("%x", sha256.Sum256(binary))
	for _, scenario := range []string{"valid", "traversal", "symlink", "duplicate", "missing", "wrong-binary"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			archive := filepath.Join(dir, "bundle.tgz")
			file, _ := os.Create(archive)
			compressed := gzip.NewWriter(file)
			writer := tar.NewWriter(compressed)
			members := map[string][]byte{"updater/updater-linux-amd64": binary, "updater/install.sh": []byte("#!/bin/sh\nexit 0\n"), "updater/systemd/updater.service": []byte("[Service]\n")}
			if scenario == "missing" {
				delete(members, "updater/install.sh")
			}
			for name, body := range members {
				_ = writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0700, Size: int64(len(body))})
				_, _ = writer.Write(body)
			}
			if scenario == "traversal" {
				_ = writer.WriteHeader(&tar.Header{Name: "../escape", Typeflag: tar.TypeReg, Size: 1})
				_, _ = writer.Write([]byte("x"))
			}
			if scenario == "symlink" {
				_ = writer.WriteHeader(&tar.Header{Name: "updater/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/shadow"})
			}
			if scenario == "duplicate" {
				_ = writer.WriteHeader(&tar.Header{Name: "updater/install.sh", Typeflag: tar.TypeReg, Size: 1})
				_, _ = writer.Write([]byte("x"))
			}
			_ = writer.Close()
			_ = compressed.Close()
			_ = file.Close()
			expected := hash
			if scenario == "wrong-binary" {
				expected = "bad"
			}
			root, err := extractInstallation(archive, filepath.Join(dir, "extracted"), expected)
			if scenario == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err = os.Stat(filepath.Join(root, "install.sh")); err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("unsafe installer accepted")
			}
		})
	}
}
