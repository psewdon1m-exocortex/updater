package component

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMastermindGryphonRegistrationIsScopedAndReusable(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("credential ownership requires an isolated root test container")
	}
	root := t.TempDir()
	clients, scoped := filepath.Join(root, "clients"), filepath.Join(root, "mastermind")
	token, err := provisionMastermindGryphon(clients, scoped, 0, 10001)
	if err != nil {
		t.Fatal(err)
	}
	again, err := provisionMastermindGryphon(clients, scoped, 0, 10001)
	if err != nil || token != again {
		t.Fatal("existing client credential was not retained")
	}
	for _, directory := range []string{clients, scoped} {
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 1 || entries[0].Name() != "mastermind.token" {
			t.Fatal("unexpected credential scope")
		}
		data, err := os.ReadFile(filepath.Join(directory, "mastermind.token"))
		if err != nil || string(data) != token {
			t.Fatal("client credentials differ")
		}
	}
}

func TestMastermindGryphonRefusesSymlinkCredential(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("credential ownership requires an isolated root test container")
	}
	root := t.TempDir()
	clients, scoped := filepath.Join(root, "clients"), filepath.Join(root, "mastermind")
	if err := os.Mkdir(clients, 0750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "untouched")
	if err := os.WriteFile(target, []byte("do-not-replace"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(clients, "mastermind.token")); err != nil {
		t.Fatal(err)
	}
	if _, err := provisionMastermindGryphon(clients, scoped, 0, 10001); err == nil {
		t.Fatal("symbolic link accepted")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "do-not-replace" {
		t.Fatal("unrelated credential changed")
	}
}
