package imagecache

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"updater/internal/config"
	"updater/internal/state"
)

func testCache(t *testing.T) (*Cache, *[]string) {
	t.Helper()
	dir := t.TempDir()
	current := "ghcr.io/psewdon1m-exocortex/kernel@sha256:" + strings.Repeat("d", 64)
	env := "KERNEL_URL=http://127.0.0.1:1\nKERNEL_SERVICE_TOKEN=test\nUPDATER_SERVICE_ID=kernel\n" +
		"UPDATER_COMPOSE_PROJECT_DIR=/opt/kernel\nUPDATER_COMPOSE_SERVICE=kernel\nUPDATER_CONTAINER_NAME=kernel\n" +
		"UPDATER_IMAGE_VARIABLE=KERNEL_IMAGE\nUPDATER_VERSION_VARIABLE=KERNEL_VERSION\nKERNEL_VERSION=4\n" +
		"UPDATER_LOCAL_HEALTH_URL=http://127.0.0.1:1/health\nUPDATER_CONTROL_TOKEN=test\nKERNEL_IMAGE=" + current + "\n"
	envPath := filepath.Join(dir, "kernel.env")
	if err := os.WriteFile(envPath, []byte(env), 0600); err != nil {
		t.Fatal(err)
	}
	runtime := config.Runtime{StateDir: dir, RegistryPath: filepath.Join(dir, "heads.json")}
	if err := config.RegisterHead(runtime.RegistryPath, "kernel", envPath); err != nil {
		t.Fatal(err)
	}
	store, err := state.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	previous := "ghcr.io/psewdon1m-exocortex/kernel@sha256:" + strings.Repeat("c", 64)
	if err := store.SaveImageGeneration("kernel", []string{previous}); err != nil {
		t.Fatal(err)
	}
	removed := []string{}
	cache := &Cache{Runtime: runtime, Store: store, Now: func() time.Time { return time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC) }}
	cache.Run = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) >= 3 && args[0] == "image" && args[1] == "ls" {
			var lines []string
			for i, letter := range []string{"a", "b", "c", "d", "e"} {
				id := "sha256:" + strings.Repeat(letter, 64)
				deleted := false
				for _, old := range removed {
					deleted = deleted || old == id
				}
				if deleted {
					continue
				}
				repository := "ghcr.io/psewdon1m-exocortex/kernel"
				if letter == "e" {
					repository = "ghcr.io/psewdon1m-exocortex/unregistered"
				}
				row, _ := json.Marshal(imageRow{ID: id, Repository: repository,
					Tag: "<none>", Digest: "sha256:" + strings.Repeat(letter, 64),
					CreatedAt: time.Date(2026, 9, 1+i, 0, 0, 0, 0, time.UTC).Format(time.RFC3339), Size: "1GB"})
				lines = append(lines, string(row))
			}
			return []byte(strings.Join(lines, "\n")), nil
		}
		if len(args) >= 1 && args[0] == "ps" {
			return nil, nil
		}
		if len(args) == 3 && args[0] == "image" && args[1] == "rm" {
			removed = append(removed, args[2])
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected Docker call: %v", args)
	}
	return cache, &removed
}

func TestPreviewAndCleanPreserveGenerationsAndScope(t *testing.T) {
	cache, removed := testCache(t)
	plan, err := cache.Preview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plan.OwnedImages != 4 || plan.ProtectedImages != 2 || len(plan.Candidates) != 2 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	for _, candidate := range plan.Candidates {
		if candidate.ID != "sha256:"+strings.Repeat("a", 64) && candidate.ID != "sha256:"+strings.Repeat("b", 64) {
			t.Fatalf("protected or unregistered image was selected: %s", candidate.ID)
		}
	}
	if _, err := cache.Clean(context.Background(), strings.Repeat("0", 64)); err == nil || len(*removed) != 0 {
		t.Fatal("stale plan caused a deletion")
	}
	result, err := cache.Clean(context.Background(), plan.ID)
	if err != nil || len(result.Removed) != 2 || len(*removed) != 2 {
		t.Fatalf("cleanup result: %+v, %v", result, err)
	}
}

func TestNoGenerationMeansNoCleanup(t *testing.T) {
	cache, _ := testCache(t)
	if err := os.Remove(filepath.Join(cache.Runtime.StateDir, "image-generations.json")); err != nil {
		t.Fatal(err)
	}
	plan, err := cache.Preview(context.Background())
	if err != nil || len(plan.Candidates) != 0 {
		t.Fatalf("missing previous generation must close cleanup: %+v, %v", plan, err)
	}
}
