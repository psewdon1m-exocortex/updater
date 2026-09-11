package engine

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
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
			if service == "saturn" {
				if _, err = os.Stat(filepath.Join(head.ProjectDir, "infra/production/Caddyfile")); !os.IsNotExist(err) {
					t.Fatal("new-only deployment file survived rollback")
				}
			}
		})
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
