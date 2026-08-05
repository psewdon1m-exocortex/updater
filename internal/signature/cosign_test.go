package signature

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnvironmentWithValueReplacesProtectedHome(t *testing.T) {
	result := environmentWithValue([]string{"PATH=/usr/bin", "HOME=/root", "LANG=C"}, "HOME", "/var/lib/updater/job-home")
	want := []string{"PATH=/usr/bin", "LANG=C", "HOME=/var/lib/updater/job-home"}
	if len(result) != len(want) {
		t.Fatalf("unexpected environment length: %#v", result)
	}
	for index := range want {
		if result[index] != want[index] {
			t.Fatalf("environment[%d] = %q, want %q", index, result[index], want[index])
		}
	}
}

func TestVerifyBlobUsesWritableOperationHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake cosign executable is a POSIX shell script")
	}
	bin := t.TempDir()
	home := filepath.Join(t.TempDir(), "sigstore-home")
	cosign := filepath.Join(bin, "cosign")
	script := "#!/bin/sh\n" +
		"test \"$HOME\" = \"$EXPECTED_HOME\" || exit 41\n" +
		"mkdir -p \"$HOME/.sigstore\" || exit 42\n"
	if err := os.WriteFile(cosign, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", "/protected/root")
	t.Setenv("EXPECTED_HOME", home)
	if err := VerifyBlob(context.Background(), "artifact", "bundle", home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".sigstore")); err != nil {
		t.Fatalf("Sigstore cache was not created in the operation home: %v", err)
	}
}
