package engine

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"updater/internal/config"
	"updater/internal/kernel"
	"updater/internal/model"
	"updater/internal/release"
	"updater/internal/state"
)

type groupRunner struct {
	mu           sync.Mutex
	calls        []string
	old          map[string]string
	version      string
	failure      string
	failed       bool
	extraService bool
}

func (r *groupRunner) Run(_ context.Context, name string, args, environment []string, directory string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	call := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	if strings.Contains(call, "config --format json") {
		env, _ := config.ParseEnvFile(filepath.Join(directory, ".env"))
		for _, value := range environment {
			key, raw, _ := strings.Cut(value, "=")
			env[key] = raw
		}
		services := map[string]any{}
		for _, component := range componentNames {
			volumes := []map[string]any{{"type": "bind", "source": filepath.Join(directory, "secrets", component), "target": "/run/mastermind", "read_only": true}}
			data := map[string]map[string]string{"core": {"core-data": "/data", "vault-data": "/data/vault"}, "runtime": {"vault-data": "/vault", "runtime-data": "/home/mastermind"}, "worker": {"work-data": "/work"}}[component]
			for source, target := range data {
				volumes = append(volumes, map[string]any{"type": "volume", "source": source, "target": target})
			}
			service := map[string]any{"image": env["MASTERMIND_"+strings.ToUpper(component)+"_IMAGE"], "user": "10001:10001", "read_only": true, "cap_drop": []string{"ALL"}, "security_opt": []string{"no-new-privileges:true"}, "volumes": volumes}
			if component == "core" {
				service["ports"] = []map[string]any{{"host_ip": "127.0.0.1", "target": 18390, "published": "18390"}}
				for _, source := range []string{"/run/neptune", "/run/exocortex"} {
					volumes = append(volumes, map[string]any{"type": "bind", "source": source, "target": source, "read_only": true})
				}
				service["volumes"] = volumes
			}
			services[component] = service
		}
		if r.extraService {
			services["unrelated"] = map[string]any{}
		}
		return json.Marshal(map[string]any{"name": "mastermind", "services": services})
	}
	if args[0] == "image" {
		if strings.Contains(call, "--format") {
			return []byte("linux/amd64"), nil
		}
		return []byte("[]"), nil
	}
	if args[0] == "inspect" {
		component := strings.TrimSuffix(strings.TrimPrefix(args[len(args)-1], "mastermind-"), "-1")
		return []byte(r.old[component]), nil
	}
	if strings.Contains(call, "update-migrate") {
		r.version = "0.0.2"
		if r.failure == "migration" || r.failure == "rollback" {
			r.failed = true
			return nil, errors.New("injected migration failure")
		}
	}
	if strings.Contains(call, "update-rollback") {
		if r.failure == "rollback" {
			return nil, errors.New("injected rollback failure")
		}
		r.version = "0.0.1"
	}
	return []byte("ok"), nil
}

func TestMastermindGroupPrepareApplyAndFailureOrdering(t *testing.T) {
	for _, failure := range []string{"none", "migration", "functional", "rollback", "changed-manifest", "extra-service", "legacy-source"} {
		t.Run(failure, func(t *testing.T) {
			directory := t.TempDir()
			old := map[string]string{}
			next := map[string]string{}
			for index, name := range componentNames {
				old[name] = "ghcr.io/test/" + name + "@sha256:" + strings.Repeat(string(rune('a'+index)), 64)
				next[name] = "ghcr.io/test/" + name + "@sha256:" + strings.Repeat(string(rune('d'+index)), 64)
			}
			runner := &groupRunner{old: old, version: "0.0.1", failure: failure, extraService: failure == "extra-service"}
			payload := []byte("PK\x03\x04sealed synthetic snapshot")
			digest := sha256.Sum256(payload)
			hash := hex.EncodeToString(digest[:])
			requestID := strings.Repeat("a", 32)
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer synthetic-control" {
					w.WriteHeader(401)
					return
				}
				var input map[string]any
				if json.NewDecoder(r.Body).Decode(&input) != nil || input["request_id"] != requestID {
					w.WriteHeader(400)
					return
				}
				runner.mu.Lock()
				defer runner.mu.Unlock()
				if r.URL.Path == "/api/internal/updater/confirm" {
					if input["sha256"] != hash || input["size"] != float64(len(payload)) {
						w.WriteHeader(409)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"held": true, "request_id": requestID, "schema": 1, "saved_copy_protocol": 2})
					return
				}
				valid := !(failure == "functional" && runner.version == "0.0.2")
				_ = json.NewEncoder(w).Encode(map[string]any{"verified": valid, "version": runner.version, "schema": 1, "vault": true, "worker": true,
					"bridge_version": runner.version, "obsidian_version": "1.13.7", "model_sha256": strings.Repeat("a", 64)})
			}))
			defer core.Close()
			env := "KERNEL_URL=http://127.0.0.1:18180\nKERNEL_SERVICE_TOKEN=fixture\nUPDATER_SERVICE_ID=mastermind\nUPDATER_COMPOSE_PROJECT_DIR=" + directory + "\nUPDATER_COMPOSE_FILE=compose.production.yaml\nUPDATER_COMPOSE_SERVICE=core\nUPDATER_CONTAINER_NAME=mastermind-core-1\nUPDATER_IMAGE_VARIABLE=MASTERMIND_CORE_IMAGE\nUPDATER_VERSION_VARIABLE=MASTERMIND_VERSION\nMASTERMIND_VERSION=0.0.1\nUPDATER_LOCAL_HEALTH_URL=" + core.URL + "/healthz\nUPDATER_CONTROL_TOKEN=synthetic-control\n"
			for key, value := range componentEnvironment(old, "0.0.1") {
				if key != "MASTERMIND_VERSION" {
					env += key + "=" + value + "\n"
				}
			}
			envPath := filepath.Join(directory, ".env")
			if err := os.WriteFile(envPath, []byte(env), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "compose.production.yaml"), []byte("original fixed profile"), 0600); err != nil {
				t.Fatal(err)
			}
			runtime := config.Runtime{StateDir: directory, RegistryPath: filepath.Join(directory, "heads.json"), UpdaterVersion: "0.4.7", CommandTimeoutSec: 10, SocketPath: "/run/exocortex/updater.sock"}
			if err := config.RegisterHead(runtime.RegistryPath, "mastermind", envPath); err != nil {
				t.Fatal(err)
			}
			store, err := state.New(directory)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.CleanupVolatileSpools() })
			instance := New(runtime, store, runner)
			instance.SetTestBackupOwnership(func(string, int, int) error { return nil })
			instance.SetTestHostOperations(func(context.Context, string) error { return nil }, nil)
			var resolved release.Resolved
			resolved.ComposePath = deploymentFixture(t, "mastermind")
			resolved.ManifestPath = filepath.Join(directory, "manifest.json")
			resolved.Manifest = model.ReleaseManifest{SchemaVersion: 1, Service: "mastermind", Version: "0.0.2", DatabaseSchema: 1, MinimumUpdaterVersion: "0.4.7"}
			resolved.Manifest.Image.Reference = "ghcr.io/test/core"
			resolved.Manifest.Image.Digest = "sha256:" + strings.Repeat("d", 64)
			resolved.Manifest.Mastermind = &model.MastermindRelease{Profile: "mastermind.components.v1", SourceSHA: strings.Repeat("a", 40), Platform: "linux/amd64", Components: next,
				BridgeVersion: "0.0.2", ObsidianVersion: "1.13.7", ModelSHA256: strings.Repeat("a", 64), MinimumSourceSchema: 1, MaximumSourceSchema: 1, SavedCopyProtocol: 2, HealthProfile: "mastermind.functional.v1",
				Dependencies: map[string]string{"kernel": "0.2.10", "volt": "0.1.5", "saturn": "0.1.15", "chronos": "0.1.1", "neptune": "0.1.7", "updater": "0.4.7"}}
			manifestBytes, _ := json.Marshal(resolved.Manifest)
			var oldManifest model.ReleaseManifest
			_ = json.Unmarshal(manifestBytes, &oldManifest)
			oldManifest.Version, oldManifest.Mastermind.BridgeVersion = "0.0.1", "0.0.1"
			oldManifest.Mastermind.Components = old
			if failure == "legacy-source" {
				oldManifest.Mastermind.SavedCopyProtocol = 0
			}
			oldManifest.Image.Digest = "sha256:" + strings.Repeat("a", 64)
			oldManifestBytes, _ := json.Marshal(oldManifest)
			if err := os.WriteFile(filepath.Join(directory, "mastermind-release.json"), oldManifestBytes, 0600); err != nil {
				t.Fatal(err)
			}
			oldDigest := sha256.Sum256(oldManifestBytes)
			if err := setEnvValues(envPath, map[string]string{"MASTERMIND_RELEASE_SHA256": hex.EncodeToString(oldDigest[:])}); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(resolved.ManifestPath, manifestBytes, 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(resolved.ManifestPath+".sig.json", []byte("signature already verified by injected release resolver"), 0600); err != nil {
				t.Fatal(err)
			}
			instance.SetTestDependencies(func(string, string, string, time.Duration) (kernel.Snapshot, error) {
				return kernel.Snapshot{Values: map[string]any{"repositories": map[string]any{"mastermind": map[string]any{"url": "https://github.com/example/mastermind"}}}}, nil
			},
				func(context.Context, string, string, string, string) (release.Resolved, error) { return resolved, nil })
			preparation, err := instance.PrepareMastermind(context.Background(), "mastermind", requestID, "0.0.2")
			if err != nil {
				t.Fatal(err)
			}
			if preparation.State != "COMPLETED" {
				t.Fatal("pre-pull was not confirmed")
			}
			if failure == "changed-manifest" {
				if err = os.WriteFile(resolved.ManifestPath, append(manifestBytes, ' '), 0600); err != nil {
					t.Fatal(err)
				}
			}
			spool, err := store.CreateSpool("mastermind", requestID, state.SpoolFilename, int64(len(payload)), hash, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err = store.UploadSpool(context.Background(), "mastermind", spool.ID, bytes.NewReader(payload), int64(len(payload))); err != nil {
				t.Fatal(err)
			}
			if _, err = store.SealSpool("mastermind", spool.ID); err != nil {
				t.Fatal(err)
			}
			input := model.UpdateRequest{HeadID: "mastermind", RequestID: requestID, Service: "mastermind", Version: "0.0.2", PreparationID: preparation.ID, Backup: model.Backup{SpoolID: spool.ID}}
			if _, err = instance.StartDownloaded(input, "synthetic-control"); err == nil {
				t.Fatal("group apply accepted without operator saved-copy proof")
			}
			proof, _ := json.Marshal(BackupReceipt{Schema: "exocortex.update-backup.v2", ID: requestID, HeadID: "mastermind", Service: "mastermind", Version: "0.0.2", Filename: state.SpoolFilename, Size: len(payload), SHA256: hash, Expires: time.Now().Add(15 * time.Minute).Unix()})
			encoded := base64.RawURLEncoding.EncodeToString(proof)
			mac := hmac.New(sha256.New, []byte("synthetic-control"))
			mac.Write([]byte(encoded))
			input.BackupReceipt, input.OperatorSaved = encoded+"."+base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), true
			if _, err = instance.StartDownloaded(input, "foreign-control"); err == nil {
				t.Fatal("wrong signer accepted")
			}
			job, err := instance.StartDownloaded(input, "synthetic-control")
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for instance.Busy() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			final, _ := store.Get(job.ID)
			expected := map[string]string{"none": "COMPLETED", "migration": "ROLLED_BACK", "functional": "ROLLED_BACK", "rollback": "ROLLBACK_FAILED", "changed-manifest": "FAILED", "extra-service": "FAILED", "legacy-source": "FAILED"}[failure]
			if final.State != expected {
				t.Fatalf("wanted %s, got %s: %s", expected, final.State, final.Message)
			}
			if _, err = os.Stat(final.BackupPath); !os.IsNotExist(err) {
				t.Fatal("terminal group retained the ZIP")
			}
			replay, replayErr := instance.StartDownloaded(input, "synthetic-control")
			if replayErr != nil || replay.ID != job.ID {
				t.Fatal("lost response replay required a deleted ZIP", replayErr)
			}
			runner.mu.Lock()
			calls := strings.Join(runner.calls, "\n")
			runner.mu.Unlock()
			if strings.Contains(calls, "down") || strings.Contains(calls, " --volumes") {
				t.Fatal("unowned/destructive Compose operation")
			}
			if failure == "changed-manifest" || failure == "extra-service" || failure == "legacy-source" {
				if strings.Contains(calls, "stop core runtime worker") {
					t.Fatal("invalid candidate stopped live components")
				}
				return
			}
			lastPull := strings.LastIndex(calls, "docker pull ")
			firstStop := strings.Index(calls, "stop core runtime worker")
			if lastPull < 0 || firstStop < lastPull {
				t.Fatal("component pull happened after the write barrier/mutation")
			}
			if expected == "ROLLED_BACK" {
				restore := strings.Index(calls, "update-rollback")
				start := strings.LastIndex(calls, "up -d --no-deps core")
				if restore < 0 || start < restore {
					t.Fatal("old Core started before data rollback")
				}
				values, _ := config.ParseEnvFile(envPath)
				for name, value := range old {
					if values["MASTERMIND_"+strings.ToUpper(name)+"_IMAGE"] != value {
						t.Fatal("partial component rollback")
					}
				}
			}
			repeated, err := instance.Start(input)
			if err != nil || repeated.ID != job.ID {
				t.Fatal("Apply retry lost durable identity")
			}
			if failure == "none" {
				rollbackRequest := strings.Repeat("b", 32)
				if _, err := instance.PrepareMastermind(context.Background(), "mastermind", rollbackRequest, "0.0.1"); err == nil {
					t.Fatal("unapproved downgrade accepted")
				}
				if _, err := instance.PrepareMastermind(context.Background(), "mastermind", rollbackRequest, "0.0.1", "foreign-job"); err == nil {
					t.Fatal("foreign rollback target accepted")
				}
				resolved.Manifest = oldManifest
				if err := os.WriteFile(resolved.ManifestPath, oldManifestBytes, 0600); err != nil {
					t.Fatal(err)
				}
				preparedRollback, err := instance.PrepareMastermind(context.Background(), "mastermind", rollbackRequest, "0.0.1", job.ID)
				if err != nil || preparedRollback.RollbackOf != job.ID || preparedRollback.PreviousVersion != "0.0.2" {
					t.Fatalf("verified previous group was not prepared: %v", err)
				}
				if err := os.WriteFile(resolved.ManifestPath, append(oldManifestBytes, ' '), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := instance.PrepareMastermind(context.Background(), "mastermind", strings.Repeat("c", 32), "0.0.1", job.ID); err == nil {
					t.Fatal("changed previous signed manifest accepted for rollback")
				}
			}
		})
	}
}
