package migration

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"errors"
	"testing"

	"updater/internal/config"
	"updater/internal/engine"
)

func TestSavedOperatorZIPUsesNormalReceiptVerification(t *testing.T) {
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	f, err := z.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte(`{"synthetic":"preserved"}`))
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	head := config.HeadConfig{ID: "laboratory-prod", Service: "laboratory", ControlToken: "synthetic-test-token"}
	request, err := prepare(head, "0.2.0", bytes.NewReader(archive.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := base64.StdEncoding.DecodeString(request.Backup.DataBase64)
	if err != nil || !bytes.Equal(actual, archive.Bytes()) {
		t.Fatal("saved ZIP was changed")
	}
	if err := engine.VerifyBackupReceipt(request, head.ControlToken); err != nil {
		t.Fatal(err)
	}
	request.Version = "0.2.1"
	if engine.VerifyBackupReceipt(request, head.ControlToken) == nil {
		t.Fatal("receipt authorized another release")
	}
	request.Version = "0.2.0"
	request.OperatorSaved = false
	if engine.VerifyBackupReceipt(request, head.ControlToken) == nil {
		t.Fatal("missing saved-copy confirmation accepted")
	}
}

type failedRead struct{}

func (failedRead) Read([]byte) (int, error) { return 0, errors.New("operator input interrupted") }

func TestMigrationRejectsMissingOrInterruptedBackup(t *testing.T) {
	head := config.HeadConfig{ID: "kernel", Service: "kernel", ControlToken: "synthetic"}
	for _, invalid := range []string{"", "not a ZIP", "PK"} {
		if _, err := prepare(head, "0.3.0", bytes.NewBufferString(invalid)); err == nil {
			t.Fatal("invalid ZIP accepted")
		}
	}
	if _, err := prepare(head, "0.3.0", failedRead{}); err == nil {
		t.Fatal("interrupted input accepted")
	}
	if _, err := Run(config.Runtime{}, []string{"--head", "kernel"}, bytes.NewBufferString("PKxx")); err == nil {
		t.Fatal("missing explicit acknowledgement accepted")
	}
}
