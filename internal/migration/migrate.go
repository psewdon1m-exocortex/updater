// Package migration bridges a saved operator backup from a protocol-1 head to
// the normal protocol-2 daemon. It never installs releases or bypasses verification.
package migration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"updater/internal/config"
	"updater/internal/engine"
	"updater/internal/model"
)

const maxBackup = 128 * 1024 * 1024

// Run accepts only a ZIP streamed back from the operator's saved copy. Reading
// the protected head configuration and acknowledging that copy require root.
func Run(runtime config.Runtime, args []string, input io.Reader) (model.Job, error) {
	var job model.Job
	if os.Geteuid() != 0 {
		return job, errors.New("migrate-head requires root")
	}
	if len(args) != 6 || args[0] != "--head" || args[2] != "--version" || args[4] != "--saved-backup-stdin" || args[5] != "--confirm-saved" {
		return job, errors.New("usage: updater migrate-head --head <id> --version <version> --saved-backup-stdin --confirm-saved")
	}
	head, err := config.LoadHead(runtime, args[1])
	if err != nil {
		return job, err
	}
	request, err := prepare(head, args[3], input)
	if err != nil {
		return job, err
	}
	body, err := json.Marshal(request)
	request.Backup.DataBase64 = ""
	if err != nil {
		return job, err
	}
	defer clear(body)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", runtime.SocketPath)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Minute}
	req, err := http.NewRequest(http.MethodPost, "http://updater.local/v2/updates", bytes.NewReader(body))
	if err != nil {
		return job, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Updater-Token", head.ControlToken)
	response, err := client.Do(req)
	if err != nil {
		return job, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return job, fmt.Errorf("migration rejected by updater: HTTP %d; no installation was accepted", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(&job); err != nil {
		return job, err
	}
	if job.ID == "" {
		return job, errors.New("updater returned no migration job ID")
	}
	return job, nil
}

func prepare(head config.HeadConfig, version string, input io.Reader) (model.UpdateRequest, error) {
	var request model.UpdateRequest
	if version == "" || head.ControlToken == "" {
		return request, errors.New("version and registered head control token are required")
	}
	data, err := io.ReadAll(io.LimitReader(input, maxBackup+1))
	defer clear(data)
	if err != nil {
		return request, err
	}
	if len(data) > maxBackup || len(data) < 4 || string(data[:2]) != "PK" {
		return request, errors.New("stdin must contain a standard ZIP backup of at most 128 MiB")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return request, err
	}
	id := hex.EncodeToString(nonce[:])
	digest := sha256.Sum256(data)
	checksum := hex.EncodeToString(digest[:])
	filename := "operator-saved-backup.zip"
	receipt := engine.BackupReceipt{Schema: "exocortex.update-backup.v2", ID: id, HeadID: head.ID, Service: head.Service, Version: version, SHA256: checksum, Size: len(data), Filename: filename, Expires: time.Now().Add(15 * time.Minute).Unix()}
	body, err := json.Marshal(receipt)
	if err != nil {
		return request, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, []byte(head.ControlToken))
	mac.Write([]byte(encoded))
	request = model.UpdateRequest{RequestID: id, HeadID: head.ID, Service: head.Service, Version: version, OperatorSaved: true, BackupReceipt: encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), Backup: model.Backup{Filename: filename, SHA256: checksum, DataBase64: base64.StdEncoding.EncodeToString(data)}}
	return request, engine.VerifyBackupReceipt(request, head.ControlToken)
}
