package component

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"time"
	"updater/internal/config"
	"updater/internal/kernel"
	"updater/internal/model"
	"updater/internal/state"
)

// ReconcileHostHelpers is called when the daemon starts and after new heads are
// registered. First-host bootstrap may await Volt/Register configuration; no
// invented repository or unverified executable is used to break that cycle.
func ReconcileHostHelpers(runtime config.Runtime, store *state.Store) {
	registry, err := config.LoadRegistry(runtime.RegistryPath)
	if err != nil {
		return
	}
	ids := []string{}
	for id := range registry.Heads {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		head, err := config.LoadHead(runtime, id)
		if err != nil {
			continue
		}
		needed := []string{}
		switch head.Service {
		case "kernel", "volt", "saturn":
			if !neptuneInstallationComplete() {
				needed = append(needed, "neptune")
			}
		}
		if head.Service == "saturn" && !gryphonInstallationComplete() {
			needed = append(needed, "gryphon")
		}
		for _, helper := range needed {
			trustDir := os.Getenv("EXOCORTEX_RELEASE_TRUST_DIR")
			if trustDir == "" {
				trustDir = "/etc/exocortex/release-trust"
			}
			if _, err := os.Stat(filepath.Join(trustDir, helper+".pem")); err != nil {
				continue
			}
			release, err := store.BeginOperation("")
			if err != nil {
				return
			}
			snapshot, err := kernel.Load(head.KernelURL, head.KernelServiceToken, head.KernelCachePath, 5*time.Second)
			if err == nil {
				_, err = kernel.String(snapshot, "repositories."+helper+".url")
			}
			if err != nil {
				release()
				continue
			}
			random := make([]byte, 16)
			if _, err = rand.Read(random); err != nil {
				release()
				return
			}
			now := time.Now().UTC()
			job := model.Job{ID: "bootstrap-" + hex.EncodeToString(random), HeadID: id, Service: helper + "-installation", State: "INSTALLING", Message: "Installing the helper required by this registered head", CreatedAt: now, UpdatedAt: now}
			if err = store.Save(job); err != nil {
				release()
				return
			}
			if helper == "neptune" {
				job.Version, err = InstallLatestNeptune(runtime, id)
			} else {
				job.Version, err = InitializeGryphon(runtime, id)
			}
			finished := time.Now().UTC()
			job.UpdatedAt, job.FinishedAt = finished, &finished
			if err != nil {
				job.State, job.Message = "FAILED", err.Error()
			} else {
				job.State, job.Message = "COMPLETED", "Required host helper installed and health verified; connection is available in Settings"
			}
			_ = store.Save(job)
			release()
			if runtime.MaxRetainedJobs > 0 && runtime.RetentionDays > 0 {
				_ = store.Prune(runtime.MaxRetainedJobs, time.Now().Add(-time.Duration(runtime.RetentionDays)*24*time.Hour))
			}
		}
	}
}
