package hostrecovery

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const windowPairingRoot = "var/lib/window-ssh/.ssh"
const windowPairingFile = windowPairingRoot + "/authorized_keys"
const windowAuthorizedPrefix = `restrict,command="/usr/local/bin/window mcp" `

type windowRecoveryUnits struct {
	Scope string   `json:"scope"`
	Units []string `json:"units"`
}

// Legacy journals remain readable. New Window operations record their scope
// even if Window was inactive, so interrupted recovery cannot stop siblings.
func decodeRecoveryUnits(body []byte) ([]string, bool, error) {
	if len(body) > 1024 {
		return nil, false, errors.New("invalid helper restart journal")
	}
	var saved []string
	windowOnly := false
	if strings.HasPrefix(strings.TrimSpace(string(body)), "{") {
		var record windowRecoveryUnits
		if json.Unmarshal(body, &record) != nil || record.Scope != "window" || len(record.Units) > 1 {
			return nil, false, errors.New("invalid Window restart journal")
		}
		saved, windowOnly = record.Units, true
	} else if json.Unmarshal(body, &saved) != nil {
		return nil, false, errors.New("invalid helper restart journal")
	}
	for _, unit := range saved {
		if windowOnly && unit != "window.service" || unit != "updater.service" && unit != "neptune.service" && unit != "gryphon.service" && unit != "wyvern.service" && unit != "window.service" {
			return nil, false, errors.New("invalid helper restart unit")
		}
	}
	return saved, windowOnly, nil
}

// Window's only recovery state is its public MCP pairing identity. A private
// SSH key, operator login, grant, lease, audit or live output is never accepted.
func windowPairingFingerprint(data []byte) (string, error) {
	if len(data) > 512 || strings.ContainsAny(string(data), "\r\x00") {
		return "", errors.New("invalid Window recovery pairing")
	}
	line := strings.TrimSuffix(string(data), "\n")
	if line == "" {
		return "", nil
	}
	if !strings.HasPrefix(line, windowAuthorizedPrefix) {
		return "", errors.New("Window recovery pairing lacks the forced read-only command")
	}
	key := strings.TrimPrefix(line, windowAuthorizedPrefix)
	parts := strings.Split(key, " ")
	if len(parts) != 3 || parts[0] != "ssh-ed25519" || parts[2] != "window-client" {
		return "", errors.New("Window recovery requires one canonical paired public key")
	}
	blob, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil || len(blob) != 51 || string(blob[:4]) != "\x00\x00\x00\x0b" || string(blob[4:15]) != "ssh-ed25519" || string(blob[15:19]) != "\x00\x00\x00\x20" || base64.StdEncoding.EncodeToString(blob) != parts[1] {
		return "", errors.New("invalid Window recovery public key")
	}
	digest := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(digest[:]), nil
}

func validateWindowEntries(entries []Entry) error {
	if len(entries) != 1 || entries[0].Name != windowPairingFile {
		return errors.New("Window recovery requires exactly one pairing identity, including an explicit empty identity when unpaired")
	}
	_, err := windowPairingFingerprint(entries[0].Data)
	return err
}

func windowRecoveryStatus(body []byte, entries []Entry) error {
	var status struct {
		Paired      *bool     `json:"paired"`
		Open        *bool     `json:"open"`
		Live        *bool     `json:"live"`
		Fingerprint string    `json:"fingerprint"`
		LeaseID     string    `json:"lease_id"`
		ExpiresAt   time.Time `json:"expires_at"`
	}
	if json.Unmarshal(body, &status) != nil || status.Paired == nil || status.Open == nil || status.Live == nil || *status.Open || *status.Live || status.LeaseID != "" || !status.ExpiresAt.IsZero() {
		return errors.New("restored Window must report closed access without a live lease")
	}
	for _, entry := range entries {
		if entry.Name == windowPairingFile {
			fingerprint, err := windowPairingFingerprint(entry.Data)
			if err != nil || *status.Paired != (fingerprint != "") || status.Fingerprint != fingerprint {
				return errors.New("restored Window pairing does not match the archive")
			}
			return nil
		}
	}
	return nil
}
