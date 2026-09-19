package engine

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"updater/internal/config"
	"updater/internal/model"
	"updater/internal/state"
)

func signedRequest(t *testing.T) model.UpdateRequest {
	t.Helper()
	data := []byte("PK\x03\x04synthetic-standard-builder-output")
	hash := sha256.Sum256(data)
	request := model.UpdateRequest{RequestID: "receipt-123456789", HeadID: "kernel", Service: "kernel", Version: "1.2.0", OperatorSaved: true, Backup: model.Backup{Filename: "kernel.zip", SHA256: hex.EncodeToString(hash[:]), DataBase64: base64.StdEncoding.EncodeToString(data)}}
	signReceipt(&request, time.Now().Add(15*time.Minute).Unix())
	return request
}

func TestRestartedEngineRequiresOriginalOperatorZipForRollback(t *testing.T) {
	e, runtime, store := testEngine(t, false)
	e.runner = successfulRunner{}
	e.SetTestHostOperations(func(context.Context, string) error { return nil }, nil)
	head, _ := config.LoadHead(runtime, "kernel")
	f, err := os.OpenFile(head.EnvFile, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("\nUPDATER_RESTORE_URL=http://127.0.0.1:1/restore\n")
	_ = f.Close()
	request := signedRequest(t)
	job, err := e.StartDownloaded(request, "control-token-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	wait := func(instance *Engine) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for instance.Busy() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if instance.Busy() {
			t.Fatal("job did not finish")
		}
	}
	wait(e)
	completed, _ := store.Get(job.ID)
	if completed.State != "COMPLETED" {
		t.Fatalf("%+v", completed)
	}
	store, err = state.New(runtime.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(runtime, store, successfulRunner{})
	var restoredPath string
	restarted.SetTestHostOperations(func(context.Context, string) error { return nil }, func(_ context.Context, _ config.HeadConfig, path string) error {
		restoredPath = path
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		original, _ := base64.StdEncoding.DecodeString(request.Backup.DataBase64)
		if string(data) != string(original) || !strings.HasPrefix(path, "/dev/shm/") {
			t.Error("restore did not receive original volatile ZIP")
		}
		return nil
	})
	if _, err = restarted.Rollback(job.ID); err == nil {
		t.Fatal("rollback without uploaded copy was accepted")
	}
	wrong := request.Backup
	wrong.SHA256 = strings.Repeat("0", 64)
	if _, err = restarted.RollbackDownloaded(job.ID, wrong); err == nil {
		t.Fatal("wrong ZIP was accepted")
	}
	if _, err = restarted.RollbackDownloaded(job.ID, request.Backup); err != nil {
		t.Fatal(err)
	}
	wait(restarted)
	completed, _ = store.Get(job.ID)
	if completed.State != "ROLLED_BACK" || restoredPath == "" || restarted.hasVolatile(job.ID) {
		t.Fatalf("rollback did not complete: %+v", completed)
	}
	if _, err = os.Stat(restoredPath); !os.IsNotExist(err) {
		t.Fatal("temporary recovery ZIP remains")
	}
}
func signReceipt(request *model.UpdateRequest, expires int64) {
	data, _ := base64.StdEncoding.DecodeString(request.Backup.DataBase64)
	body, _ := json.Marshal(BackupReceipt{Schema: "exocortex.update-backup.v2", ID: request.RequestID, HeadID: request.HeadID, Service: request.Service, Version: request.Version, SHA256: request.Backup.SHA256, Size: len(data), Filename: request.Backup.Filename, Expires: expires})
	encoded := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, []byte("control-token-long-enough"))
	mac.Write([]byte(encoded))
	request.BackupReceipt = encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func TestReceiptRejectsBypasses(t *testing.T) {
	for _, kind := range []string{"signature", "expired", "future", "head", "service", "version", "bytes", "acknowledgement"} {
		t.Run(kind, func(t *testing.T) {
			r := signedRequest(t)
			switch kind {
			case "signature":
				r.BackupReceipt += "x"
			case "expired":
				signReceipt(&r, time.Now().Add(-time.Minute).Unix())
			case "future":
				signReceipt(&r, time.Now().Add(time.Hour).Unix())
			case "head":
				r.HeadID = "other"
			case "service":
				r.Service = "volt"
			case "version":
				r.Version = "9.0.0"
			case "bytes":
				r.Backup.DataBase64 = base64.StdEncoding.EncodeToString([]byte("different"))
			case "acknowledgement":
				r.OperatorSaved = false
			}
			if VerifyBackupReceipt(r, "control-token-long-enough") == nil {
				t.Fatal("accepted invalid receipt")
			}
		})
	}
}
func TestDownloadedBackupNeverPersists(t *testing.T) {
	e, runtime, store := testEngine(t, true)
	request := signedRequest(t)
	job, err := e.StartDownloaded(request, "control-token-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for e.Busy() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	current, _ := store.Get(job.ID)
	if current.State != "COMPLETED" || current.BackupPath != "" || e.hasVolatile(job.ID) {
		t.Fatalf("unexpected completion: %+v", current)
	}
	if _, err = os.Stat(filepath.Join(runtime.StateDir, "backups")); !os.IsNotExist(err) {
		t.Fatal("archive storage must not be created")
	}
	body, _ := os.ReadFile(filepath.Join(runtime.StateDir, "jobs", job.ID+".json"))
	var saved map[string]any
	_ = json.Unmarshal(body, &saved)
	if saved["backup"] != nil || saved["data_base64"] != nil {
		t.Fatal("job exposed backup bytes")
	}
}
