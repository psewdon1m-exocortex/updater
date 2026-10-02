package imagecache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"updater/internal/config"
	"updater/internal/model"
	"updater/internal/state"
)

const imageNamespace = "ghcr.io/psewdon1m-exocortex/"
const minimumAge = 7 * 24 * time.Hour
const maximumBatch = 12

var imageID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var pullReference = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]*@sha256:[a-f0-9]{64}$`)

// Runner never invokes a shell. A test runner can supply a fixed Docker inventory.
type Runner func(context.Context, ...string) ([]byte, error)

type Image struct {
	ID         string    `json:"id"`
	Repository string    `json:"repository"`
	CreatedAt  time.Time `json:"created_at"`
	Size       string    `json:"size"`
}

type Plan struct {
	ID              string    `json:"id"`
	ObservedAt      time.Time `json:"observed_at"`
	OwnedImages     int       `json:"owned_images"`
	ProtectedImages int       `json:"protected_images"`
	Candidates      []Image   `json:"candidates"`
	Remaining       int       `json:"remaining"`
}

type Result struct {
	Removed []string `json:"removed"`
}

type Cache struct {
	Runtime config.Runtime
	Store   *state.Store
	Run     Runner
	Now     func() time.Time
}

type imageRow struct {
	ID         string `json:"ID"`
	Repository string `json:"Repository"`
	Tag        string `json:"Tag"`
	Digest     string `json:"Digest"`
	CreatedAt  string `json:"CreatedAt"`
	Size       string `json:"Size"`
}

type imageRecord struct {
	Image
	foreign bool
}

type limitedOutput struct {
	mu       sync.Mutex
	data     []byte
	exceeded bool
}

func (out *limitedOutput) Write(data []byte) (int, error) {
	out.mu.Lock()
	defer out.mu.Unlock()
	remaining := 2*1024*1024 - len(out.data)
	if len(data) > remaining {
		out.exceeded = true
	}
	out.data = append(out.data, data[:min(len(data), remaining)]...)
	return len(data), nil
}

func repositoryOf(ref string) string {
	if id := strings.IndexByte(ref, '@'); id > 0 {
		ref = ref[:id]
	} else if colon := strings.LastIndexByte(ref, ':'); colon > strings.LastIndexByte(ref, '/') {
		ref = ref[:colon]
	}
	if strings.HasPrefix(ref, imageNamespace) {
		return ref
	}
	return ""
}

func osRun(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	output := &limitedOutput{}
	cmd.Stdout, cmd.Stderr = output, output
	err := cmd.Run()
	if output.exceeded {
		return nil, errors.New("Docker inventory exceeded its output limit")
	}
	if err != nil {
		return nil, errors.New("Docker image inventory is unavailable")
	}
	return output.data, nil
}

func (c Cache) run(ctx context.Context, args ...string) ([]byte, error) {
	if c.Run != nil {
		return c.Run(ctx, args...)
	}
	return osRun(ctx, args...)
}

func createdAt(value string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05 -0700 MST", time.RFC3339, "2006-01-02 15:04:05 -0700"} {
		if result, err := time.Parse(layout, value); err == nil {
			return result.UTC(), nil
		}
	}
	return time.Time{}, errors.New("Docker image creation time is invalid")
}

func (c Cache) inventory(ctx context.Context) (map[string]*imageRecord, map[string]string, error) {
	output, err := c.run(ctx, "image", "ls", "--all", "--digests", "--no-trunc", "--format", "{{json .}}")
	if err != nil {
		return nil, nil, err
	}
	records := map[string]*imageRecord{}
	refs := map[string]string{}
	for _, line := range bytes.Split(bytes.TrimSpace(output), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var row imageRow
		if json.Unmarshal(line, &row) != nil || !imageID.MatchString(row.ID) {
			return nil, nil, errors.New("Docker returned an invalid image inventory")
		}
		created, err := createdAt(row.CreatedAt)
		if err != nil {
			return nil, nil, err
		}
		record := records[row.ID]
		if record == nil {
			record = &imageRecord{Image: Image{ID: row.ID, Repository: row.Repository, CreatedAt: created, Size: row.Size}}
			records[row.ID] = record
		}
		if row.Repository != "" && row.Repository != "<none>" && record.Repository != "" && record.Repository != "<none>" && row.Repository != record.Repository {
			record.foreign = true
		}
		if strings.HasPrefix(row.Repository, imageNamespace) {
			record.Repository = row.Repository
		}
		refs[row.ID] = row.ID
		if row.Repository != "" && row.Repository != "<none>" {
			if row.Tag != "" && row.Tag != "<none>" {
				refs[row.Repository+":"+row.Tag] = row.ID
			}
			if row.Digest != "" && row.Digest != "<none>" {
				refs[row.Repository+"@"+row.Digest] = row.ID
			}
		}
	}
	return records, refs, nil
}

func (c Cache) protectReference(ctx context.Context, ref string, refs map[string]string, protected map[string]bool, required bool) error {
	if ref == "" {
		return nil
	}
	if id := refs[ref]; id != "" {
		protected[id] = true
		return nil
	}
	// An old deployment can be identified by its local image ID even when its
	// tag disappeared. Never guess the ID from a human-readable repository name.
	output, err := c.run(ctx, "image", "inspect", "--format", "{{.Id}}", ref)
	id := strings.TrimSpace(string(output))
	if err == nil && imageID.MatchString(id) {
		protected[id] = true
		return nil
	}
	if required {
		return errors.New("a protected deployment image cannot be resolved; cleanup is disabled")
	}
	return nil
}

func jobReferences(job model.Job) []string {
	refs := []string{job.PreviousImage, job.PreviousWebImage, job.RecoveryImage}
	for _, ref := range job.PreviousComponents {
		refs = append(refs, ref)
	}
	for _, ref := range job.ComponentImages {
		refs = append(refs, ref)
	}
	return refs
}

func jobCanPullPrevious(job model.Job) bool {
	if !pullReference.MatchString(job.PreviousImagePull) {
		return false
	}
	if job.PreviousWebImage != "" && !pullReference.MatchString(job.PreviousWebImage) {
		return false
	}
	for _, ref := range job.PreviousComponents {
		if !pullReference.MatchString(ref) {
			return false
		}
	}
	return true
}

func (c Cache) Preview(ctx context.Context) (Plan, error) {
	if c.Store == nil {
		return Plan{}, errors.New("Updater job store is unavailable")
	}
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	records, refs, err := c.inventory(ctx)
	if err != nil {
		return Plan{}, err
	}
	protected := map[string]bool{}
	containerIDs, err := c.run(ctx, "ps", "-a", "-q", "--no-trunc")
	if err != nil {
		return Plan{}, err
	}
	ids := strings.Fields(string(containerIDs))
	if len(ids) != 0 {
		args := append([]string{"inspect", "--format", "{{.Image}}"}, ids...)
		images, err := c.run(ctx, args...)
		if err != nil {
			return Plan{}, err
		}
		for _, id := range strings.Fields(string(images)) {
			if !imageID.MatchString(id) {
				return Plan{}, errors.New("Docker returned an invalid container image ID")
			}
			protected[id] = true
		}
	}
	registry, err := config.LoadRegistry(c.Runtime.RegistryPath)
	if err != nil {
		return Plan{}, err
	}
	generations, err := c.Store.ImageGenerations()
	if err != nil {
		return Plan{}, err
	}
	jobs := c.Store.List()
	latest := map[string]time.Time{}
	previousJob := map[string]bool{}
	for _, job := range jobs {
		if job.RollbackAvailable && job.FinishedAt != nil && job.UpdatedAt.After(latest[job.HeadID]) {
			latest[job.HeadID] = job.UpdatedAt
			previousJob[job.HeadID] = job.PreviousImage != "" || len(job.PreviousComponents) > 0
		}
	}
	eligibleRepository := map[string]bool{}
	for id := range registry.Heads {
		head, err := config.LoadHead(c.Runtime, id)
		if err != nil {
			return Plan{}, errors.New("registered deployment configuration is incomplete; cleanup is disabled")
		}
		values, err := config.ParseEnvFile(head.EnvFile)
		if err != nil {
			return Plan{}, err
		}
		current := []string{values[head.ImageVariable]}
		if head.Service == "saturn" {
			current = append(current, values["VAULT_WEB_IMAGE"])
		}
		if head.Service == "mastermind" {
			current = append(current, values["MASTERMIND_CORE_IMAGE"], values["MASTERMIND_RUNTIME_IMAGE"], values["MASTERMIND_WORKER_IMAGE"])
		}
		for _, ref := range current {
			if ref == "" {
				return Plan{}, errors.New("registered image reference is empty; cleanup is disabled")
			}
			if err := c.protectReference(ctx, ref, refs, protected, true); err != nil {
				return Plan{}, err
			}
			if len(generations[id]) != 0 || previousJob[id] {
				if repository := repositoryOf(ref); repository != "" {
					eligibleRepository[repository] = true
				}
			}
		}
		for _, ref := range generations[id] {
			if err := c.protectReference(ctx, ref, refs, protected, true); err != nil {
				return Plan{}, err
			}
		}
	}
	for _, job := range jobs {
		if !job.RollbackAvailable && job.FinishedAt != nil && !job.RecoveryPending {
			continue
		}
		required := job.FinishedAt == nil || job.RecoveryPending || job.UpdatedAt.Equal(latest[job.HeadID]) || !jobCanPullPrevious(job)
		if required {
			for _, ref := range jobReferences(job) {
				if err := c.protectReference(ctx, ref, refs, protected, true); err != nil {
					return Plan{}, err
				}
			}
		}
	}
	byRepository := map[string][]*imageRecord{}
	for _, record := range records {
		if eligibleRepository[record.Repository] && !record.foreign {
			byRepository[record.Repository] = append(byRepository[record.Repository], record)
		}
	}
	for _, items := range byRepository {
		sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
		for _, item := range items[:min(2, len(items))] {
			protected[item.ID] = true
		}
	}
	plan := Plan{ObservedAt: now, Candidates: []Image{}}
	var signature strings.Builder
	allIDs := make([]string, 0, len(records))
	for id := range records {
		allIDs = append(allIDs, id)
	}
	sort.Strings(allIDs)
	for _, id := range allIDs {
		item := records[id]
		fmt.Fprintf(&signature, "%s:%t:%s\n", id, protected[id], item.Repository)
		if !eligibleRepository[item.Repository] || item.foreign {
			continue
		}
		plan.OwnedImages++
		if protected[id] {
			plan.ProtectedImages++
			continue
		}
		if now.Sub(item.CreatedAt) < minimumAge {
			continue
		}
		plan.Candidates = append(plan.Candidates, item.Image)
	}
	sort.Slice(plan.Candidates, func(i, j int) bool { return plan.Candidates[i].CreatedAt.Before(plan.Candidates[j].CreatedAt) })
	if len(plan.Candidates) > maximumBatch {
		plan.Remaining = len(plan.Candidates) - maximumBatch
		plan.Candidates = plan.Candidates[:maximumBatch]
	}
	for _, item := range plan.Candidates {
		signature.WriteString(item.ID)
	}
	digest := sha256.Sum256([]byte(signature.String()))
	plan.ID = hex.EncodeToString(digest[:])
	return plan, nil
}

func (c Cache) Clean(ctx context.Context, expectedID string) (Result, error) {
	if c.Store == nil {
		return Result{}, errors.New("Updater job store is unavailable")
	}
	if len(expectedID) != 64 {
		return Result{}, errors.New("image cleanup plan ID is invalid")
	}
	release, err := c.Store.BeginOperation("")
	if err != nil {
		return Result{}, err
	}
	defer release()
	plan, err := c.Preview(ctx)
	if err != nil {
		return Result{}, err
	}
	if plan.ID != expectedID {
		return Result{}, errors.New("image inventory changed; review a new cleanup plan")
	}
	result := Result{Removed: []string{}}
	for _, item := range plan.Candidates {
		if !imageID.MatchString(item.ID) {
			return result, errors.New("image cleanup candidate is invalid")
		}
		if _, err := c.run(ctx, "image", "rm", item.ID); err != nil {
			return result, errors.New("Docker refused image cleanup; refresh the plan")
		}
		result.Removed = append(result.Removed, item.ID)
	}
	return result, nil
}
