package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"updater/internal/config"
)

func TestHeadEnvironmentMergeAndRollback(t *testing.T) {
	for _, service := range []string{"chronos", "laboratory"} {
		dir := t.TempDir()
		file := filepath.Join(dir, ".env")
		original := []byte("# operator values\nKERNEL_URL=https://operator.example.test\nACCESS_KEY=synthetic-retained\n")
		if err := os.WriteFile(file, original, 0600); err != nil {
			t.Fatal(err)
		}
		head := config.HeadConfig{Service: service, ProjectDir: dir, EnvFile: file, ComposeFile: "compose.production.yaml"}
		files := map[string][]byte{"compose.production.yaml": []byte("services: {}\n"), ".env.example": []byte("KERNEL_URL=https://template.invalid\nACCESS_KEY=CHANGE_ME\nNEW_DEFAULT=42\nNEW_SECRET=CHANGE_ME\n")}
		if err := prepareHeadEnvironment(head, files); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(files[".env"]), string(original)) || !strings.Contains(string(files[".env"]), "NEW_DEFAULT=42") || strings.Contains(string(files[".env"]), "NEW_SECRET") {
			t.Fatal("operator/default ownership violation")
		}
		snapshot := filepath.Join(dir, "deployment.json")
		if err := snapshotDeployment(head, files, snapshot); err != nil {
			t.Fatal(err)
		}
		saved, _ := os.ReadFile(snapshot)
		if strings.Contains(string(saved), "synthetic-retained") {
			t.Fatal("snapshot duplicated environment secrets")
		}
		if err := applyDeployment(head, files); err != nil {
			t.Fatal(err)
		}
		if err := restoreDeployment(head, snapshot); err != nil {
			t.Fatal(err)
		}
		actual, _ := os.ReadFile(file)
		if string(actual) != string(original) {
			t.Fatal("rollback did not restore exact operator environment")
		}
	}
}
