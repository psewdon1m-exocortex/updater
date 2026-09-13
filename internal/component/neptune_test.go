package component

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGithubRepositoryAndVersionOrdering(t *testing.T) {
	owner, repository, err := githubRepository("https://github.com/psewdon1m-exocortex/neptune.git")
	if err != nil || owner != "psewdon1m-exocortex" || repository != "neptune" {
		t.Fatalf("unexpected repository parse: %q %q %v", owner, repository, err)
	}
	if _, _, err := githubRepository("http://github.com/example/neptune"); err == nil {
		t.Fatal("non-HTTPS repository was accepted")
	}
	if compareVersion("1.2.0", "1.1.9") <= 0 || compareVersion("1.2.0", "1.2.0-dev") <= 0 {
		t.Fatal("semantic version ordering is incorrect")
	}
}

func TestLatestQualifiedReleaseVersionRejectsLegacyAndUnpublishedTags(t *testing.T) {
	releases := []githubRelease{
		{TagName: "neptune-linux-v9.9.9"},
		{TagName: "neptune-v0.1.4"},
		{TagName: "neptune-v0.1.5", Draft: true},
		{TagName: "neptune-v0.1.6", Prerelease: true},
		{TagName: "neptune-v0.2.0"},
		{TagName: "gryphon-linux-v8.8.8"},
		{TagName: "gryphon-v0.1.2"},
	}
	if actual := latestQualifiedReleaseVersion(releases, neptuneReleaseTagPrefix); actual != "0.2.0" {
		t.Fatalf("unexpected Neptune candidate %q", actual)
	}
	if actual := latestQualifiedReleaseVersion(releases, gryphonReleaseTagPrefix); actual != "0.1.2" {
		t.Fatalf("unexpected Gryphon candidate %q", actual)
	}
}

func TestUpdateEnvFilePreservesUnrelatedValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("# operator input\nKEEP=value\nNEPTUNE_SOCKET_GID=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := updateEnvFile(path, map[string]string{"NEPTUNE_SOCKET_GID": "1200", "NEPTUNE_PROJECT_ID": "chronos"}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, expected := range []string{"# operator input", "KEEP=value", "NEPTUNE_SOCKET_GID=1200", "NEPTUNE_PROJECT_ID=chronos"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("updated env is missing %q: %s", expected, text)
		}
	}
}

func TestExtractNeptuneBinarySelectsOnlyExactEntry(t *testing.T) {
	directory := t.TempDir()
	archivePath := filepath.Join(directory, "release.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	payload := []byte("neptune-binary")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "./neptuned", Mode: 0o755, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "neptuned")
	if err := extractNeptuneBinary(archivePath, target); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(payload) {
		t.Fatalf("unexpected payload %q", actual)
	}
}
