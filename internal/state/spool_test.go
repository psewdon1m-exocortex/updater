//go:build linux

package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"updater/internal/model"
)

func fixtureSpool(t *testing.T) (*Store, Spool, []byte) {
	t.Helper()
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.CleanupVolatileSpools() })
	body := bytes.Repeat([]byte("opaque encrypted recovery bytes\n"), 100)
	sum := sha256.Sum256(body)
	item, err := store.CreateSpool("mastermind", "request-one", SpoolFilename, int64(len(body)), hex.EncodeToString(sum[:]), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return store, item, body
}

func TestSpoolRestartRetrySealBindingAndRetention(t *testing.T) {
	store, item, body := fixtureSpool(t)
	if err := store.UploadSpool(context.Background(), item.HeadID, item.ID, bytes.NewReader(body[:10]), item.Size); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	if _, err := store.SealSpool(item.HeadID, item.ID); err == nil {
		t.Fatal("incomplete upload sealed")
	}
	restarted, err := New(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	same, err := restarted.CreateSpool(item.HeadID, item.RequestID, item.Filename, item.Size, item.SHA256, time.Now())
	if err != nil || same.ID != item.ID {
		t.Fatal("retry did not preserve reservation", err)
	}
	if err = restarted.UploadSpool(context.Background(), item.HeadID, item.ID, bytes.NewReader(body), item.Size); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.SealSpool("other-head", item.ID); err == nil {
		t.Fatal("wrong head sealed")
	}
	if _, err = restarted.SealSpool(item.HeadID, item.ID); err != nil {
		t.Fatal(err)
	}
	if err = restarted.UploadSpool(context.Background(), item.HeadID, item.ID, bytes.NewReader(body), item.Size); err == nil {
		t.Fatal("sealed upload overwritten")
	}
	if _, err = restarted.ClaimSpool(item.HeadID, "other-request", item.ID, "job-one"); err == nil {
		t.Fatal("cross-request claim accepted")
	}
	path, err := restarted.ClaimSpool(item.HeadID, item.RequestID, item.ID, "job-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.ClaimSpool(item.HeadID, item.RequestID, item.ID, "job-two"); err == nil {
		t.Fatal("second job reused spool")
	}
	if _, err = restarted.ClaimSpool(item.HeadID, item.RequestID, item.ID, "job-one"); err != nil {
		t.Fatal("same job retry rejected", err)
	}
	finished := time.Now().Add(-25 * time.Hour)
	if err = restarted.Save(model.Job{ID: "job-one", HeadID: item.HeadID, RequestID: item.RequestID, State: "ROLLBACK_FAILED", UpdatedAt: finished, FinishedAt: &finished, BackupPath: path, BackupSpoolID: item.ID}); err != nil {
		t.Fatal(err)
	}
	if err = restarted.Prune(1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("failed job retained ZIP bytes instead of requiring the saved operator copy", err)
	}
	job, _ := restarted.Get("job-one")
	if job.BackupSpoolID != item.ID {
		t.Fatal("recovery identity was removed")
	}
	job.State = "ROLLED_BACK"
	if err = restarted.Save(job); err != nil {
		t.Fatal(err)
	}
	if err = restarted.PruneSpools(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("completed backup retained beyond its bound")
	}
}

func TestSpoolHostilePathsTamperQuotaAndExpiry(t *testing.T) {
	store, item, body := fixtureSpool(t)
	store.spoolQuota = item.Size
	if _, err := store.CreateSpool("mastermind", "second", SpoolFilename, 1, item.SHA256, time.Now()); !errors.Is(err, ErrSpoolQuota) {
		t.Fatal(err)
	}
	for _, id := range []string{"../outside", "/absolute", item.ID + "/file"} {
		if _, err := store.SealSpool(item.HeadID, id); err == nil {
			t.Fatal("unsafe spool ID")
		}
	}
	root, _ := store.spoolRoot()
	target := filepath.Join(root, item.ID, SpoolFilename)
	outside := root + "-outside"
	t.Cleanup(func() { _ = os.Remove(outside) })
	if err := os.WriteFile(outside, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SealSpool(item.HeadID, item.ID); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := store.UploadSpool(context.Background(), item.HeadID, item.ID, bytes.NewReader(body), item.Size); err == nil {
		t.Fatal("symlink replaced")
	}
	os.Remove(target)
	if err := os.Link(outside, target); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SealSpool(item.HeadID, item.ID); err == nil {
		t.Fatal("hardlink accepted")
	}
	os.Remove(target)
	corrupt := bytes.Repeat([]byte("x"), len(body))
	if err := store.UploadSpool(context.Background(), item.HeadID, item.ID, bytes.NewReader(corrupt), item.Size); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SealSpool(item.HeadID, item.ID); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
	if err := store.PruneSpools(time.Now().Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("expired incomplete upload retained")
	}
	if after, _ := os.ReadFile(outside); !bytes.Equal(after, body) {
		t.Fatal("outside file was modified")
	}
}

type zeroStream struct{}

func (zeroStream) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestSpoolLarge356MiBHasBoundedAllocations(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.CleanupVolatileSpools() })
	const size = int64(356 * 1024 * 1024)
	hash := sha256.New()
	if _, err = io.CopyBuffer(hash, io.LimitReader(zeroStream{}, size), make([]byte, 1024*1024)); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	item, err := store.CreateSpool("mastermind", "large-356", SpoolFilename, size, hex.EncodeToString(hash.Sum(nil)), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = store.UploadSpool(context.Background(), item.HeadID, item.ID, io.LimitReader(zeroStream{}, size), size); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SealSpool(item.HeadID, item.ID); err != nil {
		t.Fatal(err)
	}
	path, err := store.ClaimSpool(item.HeadID, item.RequestID, item.ID, "large-job")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != size {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	if allocated > 16*1024*1024 {
		t.Fatalf("streaming allocated %d bytes", allocated)
	}
	fmt.Printf("356 MiB upload/seal/claim: %s, additional Go allocations %d bytes\n", time.Since(started), allocated)
}
