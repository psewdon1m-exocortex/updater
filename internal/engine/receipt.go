package engine

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"updater/internal/model"
)

// The authenticated head signs the exact ZIP handed to the operator. The
// browser returns those bytes after saving; neither receipts nor jobs contain
// their contents. Possession of only a backup id never authorizes installation.
type BackupReceipt struct {
	Schema   string `json:"schema"`
	ID       string `json:"id"`
	HeadID   string `json:"head_id"`
	Service  string `json:"service"`
	Version  string `json:"version"`
	SHA256   string `json:"sha256"`
	Size     int    `json:"size"`
	Filename string `json:"filename"`
	Expires  int64  `json:"expires"`
}

func VerifyBackupReceipt(request model.UpdateRequest, token string) error {
	parts := strings.Split(request.BackupReceipt, ".")
	if len(parts) != 2 || !request.OperatorSaved || token == "" {
		return errors.New("save the ZIP on your computer before installing")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(body) > 4096 {
		return errors.New("invalid backup receipt")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return errors.New("invalid backup receipt")
	}
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(parts[0]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return errors.New("invalid backup receipt signature")
	}
	var receipt BackupReceipt
	if json.Unmarshal(body, &receipt) != nil || receipt.Schema != "exocortex.update-backup.v2" || receipt.Expires < time.Now().Unix() || receipt.Expires > time.Now().Add(20*time.Minute).Unix() {
		return errors.New("backup receipt expired or invalid; create a fresh ZIP")
	}
	if receipt.HeadID != request.HeadID || receipt.Service != request.Service || receipt.Version != request.Version || receipt.ID != request.RequestID || receipt.Filename != request.Backup.Filename {
		return errors.New("backup receipt does not match this installation")
	}
	data, err := decodeBackup(request.Backup)
	if err != nil {
		return err
	}
	defer clear(data)
	digest := sha256.Sum256(data)
	if receipt.SHA256 != hex.EncodeToString(digest[:]) || receipt.Size != len(data) {
		return errors.New("the supplied ZIP differs from the downloaded backup")
	}
	if len(data) < 4 || string(data[:2]) != "PK" || !strings.HasSuffix(receipt.Filename, ".zip") {
		return errors.New("a standard ZIP backup is required")
	}
	return nil
}
