package engine

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"updater/internal/config"
)

func deploymentFixture(t *testing.T, service string) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "bundle")
	f, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	if service == "saturn" {
		w := zip.NewWriter(f)
		for name := range deploymentNames(service) {
			out, err := w.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = out.Write([]byte("new release configuration\n")); err != nil {
				t.Fatal(err)
			}
		}
		if err = w.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		g := gzip.NewWriter(f)
		w := tar.NewWriter(g)
		data := []byte("services: {kernel: {image: next}}\n")
		if err = w.WriteHeader(&tar.Header{Name: "./compose.production.yaml", Mode: 0600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(data); err != nil {
			t.Fatal(err)
		}
		if err = w.Close(); err != nil {
			t.Fatal(err)
		}
		if err = g.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	return filename
}

func TestDeploymentSwitchAndRollbackPreserveEnvironment(t *testing.T) {
	for _, service := range []string{"kernel", "volt", "saturn"} {
		t.Run(service, func(t *testing.T) {
			head := config.HeadConfig{Service: service, ProjectDir: t.TempDir(), ComposeFile: "compose.production.yaml"}
			compose := filepath.Join(head.ProjectDir, head.ComposeFile)
			if err := os.WriteFile(compose, []byte("old compose"), 0600); err != nil {
				t.Fatal(err)
			}
			env := filepath.Join(head.ProjectDir, ".env")
			if err := os.WriteFile(env, []byte("operator-owned"), 0600); err != nil {
				t.Fatal(err)
			}
			files, err := readDeployment(deploymentFixture(t, service), service)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := filepath.Join(t.TempDir(), "deployment.json")
			if err = snapshotDeployment(head, files, snapshot); err != nil {
				t.Fatal(err)
			}
			if err = applyDeployment(head, files); err != nil {
				t.Fatal(err)
			}
			if data, _ := os.ReadFile(compose); string(data) == "old compose" {
				t.Fatal("release deployment not applied")
			}
			if err = restoreDeployment(head, snapshot); err != nil {
				t.Fatal(err)
			}
			if data, _ := os.ReadFile(compose); string(data) != "old compose" {
				t.Fatal("old deployment not restored")
			}
			if data, _ := os.ReadFile(env); string(data) != "operator-owned" {
				t.Fatal("operator environment changed")
			}
		})
	}
}

func TestSaturnDeploymentRequiresOnlyComposeAndIgnoresRetiredCaddyfile(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "saturn.zip")
	f, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for name, data := range map[string]string{
		"compose.production.yaml":        "services: {}\n",
		"infra/production/Caddyfile":     "retired embedded proxy\n",
		"infra/production/nginx.example": "operator reference only\n",
	} {
		out, createErr := w.Create(name)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := out.Write([]byte(data)); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}

	files, err := readDeployment(filename, "saturn")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || string(files["compose.production.yaml"]) != "services: {}\n" {
		t.Fatalf("unexpected Saturn deployment files: %#v", files)
	}
}

func TestSaturnLegacySnapshotRestoresComposeWithoutTouchingCaddyfile(t *testing.T) {
	dir := t.TempDir()
	head := config.HeadConfig{Service: "saturn", ProjectDir: dir, ComposeFile: "compose.production.yaml"}
	compose := filepath.Join(dir, head.ComposeFile)
	if err := os.WriteFile(compose, []byte("new compose"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(dir, "deployment.json")
	legacy, err := json.Marshal([]deploymentFile{
		{Name: "compose.production.yaml", Existed: true, Data: []byte("old compose")},
		{Name: "infra/production/Caddyfile", Existed: true, Data: []byte("old caddy")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(snapshot, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if err = restoreDeployment(head, snapshot); err != nil {
		t.Fatal(err)
	}
	if data, readErr := os.ReadFile(compose); readErr != nil || string(data) != "old compose" {
		t.Fatalf("Compose was not restored: %q, %v", data, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "infra", "production", "Caddyfile")); !os.IsNotExist(statErr) {
		t.Fatal("retired Caddyfile was restored")
	}
}

func TestDeploymentRejectsUnsafeAndIncompleteBundles(t *testing.T) {
	for _, name := range []string{"../compose.production.yaml", "/compose.production.yaml", "README.md"} {
		t.Run(name, func(t *testing.T) {
			f, _ := os.Create(filepath.Join(t.TempDir(), "bad.zip"))
			w := zip.NewWriter(f)
			out, _ := w.Create(name)
			out.Write([]byte("bad"))
			w.Close()
			f.Close()
			if _, err := readDeployment(f.Name(), "saturn"); err == nil {
				t.Fatal("unsafe or incomplete bundle accepted")
			}
		})
	}
}
