package component

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestGryphonRepositoryRequiresApprovedGitHubShape(t *testing.T) {
	owner, repository, err := gryphonRepository("https://github.com/example/gryphon.git")
	if err != nil || owner != "example" || repository != "gryphon" {
		t.Fatalf("unexpected repository parse: %q %q %v", owner, repository, err)
	}
	if _, _, err := gryphonRepository("https://example.com/example/gryphon"); err == nil {
		t.Fatal("non-GitHub repository was accepted")
	}
}

func TestExtractGryphonAppValidatesPackageIdentity(t *testing.T) {
	directory := t.TempDir()
	archivePath := filepath.Join(directory, "release.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	entries := map[string]string{
		"package.json": `{"name":"@exocortex/gryphon","version":"1.2.3"}`,
		"dist/main.js": "console.log('daemon')",
		"dist/cli.js":  "console.log('cli')",
		"ignored.txt":  "must not be extracted",
	}
	for name, value := range entries {
		payload := []byte(value)
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(payload); err != nil {
			t.Fatal(err)
		}
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
	target := filepath.Join(directory, "app")
	if err := extractGryphonApp(archivePath, target, "1.2.3"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "ignored.txt")); !os.IsNotExist(err) {
		t.Fatal("unexpected release member was extracted")
	}
	if err := extractGryphonApp(archivePath, filepath.Join(directory, "wrong"), "1.2.4"); err == nil {
		t.Fatal("mismatched package version was accepted")
	}
}
