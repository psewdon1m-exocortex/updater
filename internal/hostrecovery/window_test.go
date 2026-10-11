package hostrecovery

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func syntheticWindowPairing(phase string) []byte {
	blob := append([]byte("\x00\x00\x00\x0bssh-ed25519\x00\x00\x00\x20"), bytes.Repeat([]byte{phase[0]}, 32)...)
	return []byte(windowAuthorizedPrefix + "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " window-client\n")
}

func TestWindowRecoveryUnpairedSnapshotReplacesLaterPairing(t *testing.T) {
	source := t.TempDir()
	// Neither unrelated runtime state nor operator SSH credentials belong to
	// this public identity snapshot, even when they exist beside the pairing.
	for _, name := range []string{"var/lib/window/audit.jsonl", "var/lib/window/test-results/test.json", "home/windowops/.ssh/authorized_keys", "etc/shadow"} {
		filename := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte("excluded data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := CollectScope(source, "window")
	if err != nil || len(entries) != 1 || entries[0].Name != windowPairingFile || len(entries[0].Data) != 0 {
		t.Fatal("missing explicit unpaired identity", entries, err)
	}
	archive, err := SealScope(entries, "synthetic recovery passphrase", "window")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := OpenScope(archive, "synthetic recovery passphrase", "window")
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	fixture := hostRecoveryFixture("live")
	writeHostRecoveryFixture(t, target, fixture)
	if err := ApplyScoped(target, decoded, "window", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	fixture["window"][windowPairingFile] = nil
	assertHostRecoveryFixture(t, target, fixture)
}

// Models a decryptable but invalid payload from outside the trusted exporter.
func untrustedWindowArchive(t *testing.T, entries []Entry) []byte {
	t.Helper()
	var payload bytes.Buffer
	w := zip.NewWriter(&payload)
	for _, entry := range entries {
		member, err := w.Create(entry.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := member.Write(entry.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	salt, nonce := make([]byte, 16), make([]byte, 12)
	key, err := pbkdf2.Key(sha256.New, "synthetic recovery passphrase", salt, 600000, 32)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	header := scopedHeader("window", salt, nonce)
	return gcm.Seal(header, nonce, payload.Bytes(), header)
}

func TestWindowRecoveryRejectsInvalidIdentitiesBeforeMutation(t *testing.T) {
	valid := Entry{Name: windowPairingFile, Data: syntheticWindowPairing("snapshot")}
	cases := map[string][]Entry{
		"missing identity":    nil,
		"duplicate identity":  {valid, valid},
		"private key":         {{Name: windowPairingRoot + "/id_ed25519", Data: []byte("private")}},
		"grant":               {{Name: "var/lib/window/lease.json", Data: []byte(`{"open":true}`)}},
		"operator login":      {{Name: "home/windowops/.ssh/authorized_keys", Data: valid.Data}},
		"another service":     {{Name: "var/lib/gryphon/state.json", Data: []byte(`{}`)}},
		"directory traversal": {{Name: windowPairingRoot + "/../authorized_keys", Data: valid.Data}},
		"extra command":       {{Name: valid.Name, Data: bytes.Replace(valid.Data, []byte("window mcp"), []byte("window observe"), 1)}},
		"ssh options":         {{Name: valid.Name, Data: append([]byte("environment=ROOT=1,"), valid.Data...)}},
		"two keys":            {{Name: valid.Name, Data: append(bytes.Clone(valid.Data), valid.Data...)}},
		"bad key":             {{Name: valid.Name, Data: []byte(windowAuthorizedPrefix + "ssh-ed25519 invalid window-client\n")}},
		"extra comment":       {{Name: valid.Name, Data: bytes.Replace(valid.Data, []byte("window-client"), []byte("window-client extra"), 1)}},
		"oversized":           {{Name: valid.Name, Data: bytes.Repeat([]byte("x"), 513)}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := SealScope(entries, "synthetic recovery passphrase", "window"); err == nil {
				t.Fatal("export accepted invalid Window identity")
			}
			if _, err := OpenScope(untrustedWindowArchive(t, entries), "synthetic recovery passphrase", "window"); err == nil {
				t.Fatal("import accepted invalid Window identity")
			}
			root := t.TempDir()
			fixture := hostRecoveryFixture("live")
			writeHostRecoveryFixture(t, root, fixture)
			if err := ApplyScoped(root, entries, "window", func() error { t.Fatal("invalid restore reached verification"); return nil }); err == nil {
				t.Fatal("invalid restore mutated live state")
			}
			assertHostRecoveryFixture(t, root, fixture)
		})
	}
}

func TestWindowRecoveryRejectsSymlinkAndUnexpectedState(t *testing.T) {
	for _, name := range []string{windowPairingFile, "var/lib/window-ssh"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			link := filepath.Join(root, name)
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), link); err != nil {
				t.Fatal(err)
			}
			if _, err := CollectScope(root, "window"); err == nil {
				t.Fatal("followed a pairing symlink")
			}
		})
	}
	root := t.TempDir()
	writeHostRecoveryFixture(t, root, map[string]map[string][]byte{"window": {windowPairingRoot + "/private.key": []byte("private")}})
	if _, err := CollectScope(root, "window"); err == nil {
		t.Fatal("silently accepted or omitted unexpected pairing files")
	}
}

func TestWindowRecoveryRequiresClosedHealthAndMatchingIdentity(t *testing.T) {
	entry := Entry{Name: windowPairingFile, Data: syntheticWindowPairing("snapshot")}
	fingerprint, err := windowPairingFingerprint(entry.Data)
	if err != nil {
		t.Fatal(err)
	}
	good := map[string]any{"paired": true, "open": false, "live": false, "fingerprint": fingerprint}
	body, _ := json.Marshal(good)
	if err := windowRecoveryStatus(body, []Entry{entry}); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"open": true, "live": true, "lease_id": "old-lease", "expires_at": "2026-10-10T18:00:00Z", "fingerprint": "SHA256:other", "paired": false} {
		t.Run(name, func(t *testing.T) {
			bad := map[string]any{}
			for key, value := range good {
				bad[key] = value
			}
			bad[name] = value
			body, _ := json.Marshal(bad)
			if err := windowRecoveryStatus(body, []Entry{entry}); err == nil {
				t.Fatal("unsafe restored state passed verification")
			}
		})
	}
	for _, body := range []string{"{}", "not-json", `{"open":false,"live":false}`} {
		if err := windowRecoveryStatus([]byte(body), []Entry{entry}); err == nil {
			t.Fatal("incomplete health passed verification")
		}
	}
	if err := windowRecoveryStatus([]byte(`{"paired":false,"open":false,"live":false}`), []Entry{{Name: windowPairingFile}}); err != nil {
		t.Fatal("unpaired restore was rejected", err)
	}
	if err := windowRecoveryStatus(body, []Entry{{Name: windowPairingFile}}); err == nil {
		t.Fatal("unpaired restore preserved a later pairing")
	}
}

func TestLegacyRecoveryWithoutWindowPreservesItsPairing(t *testing.T) {
	root := t.TempDir()
	fixture := hostRecoveryFixture("live")
	writeHostRecoveryFixture(t, root, fixture)
	entries, err := Collect(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name == windowPairingFile {
			t.Fatal("legacy collection unexpectedly adopted Window state")
		}
	}
	if _, err := Seal([]Entry{{Name: windowPairingFile, Data: syntheticWindowPairing("snapshot")}}, "synthetic recovery passphrase"); err == nil {
		t.Fatal("Window state was accepted into a legacy archive")
	}
	if err := Apply(root, []Entry{{Name: "var/lib/neptune/projects.json", Data: []byte(`{}`)}}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, windowPairingFile))
	if err != nil || !bytes.Equal(body, fixture["window"][windowPairingFile]) {
		t.Fatal("legacy archive erased a separately installed Window pairing", err)
	}
}

func TestWindowInterruptedRecoveryRestoresPreviousPairing(t *testing.T) {
	root := t.TempDir()
	fixture := hostRecoveryFixture("live")
	writeHostRecoveryFixture(t, root, fixture)
	target := filepath.Join(root, windowPairingRoot)
	if err := os.Rename(target, target+".recovery-previous"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "authorized_keys"), syntheticWindowPairing("snapshot"), 0644); err != nil {
		t.Fatal(err)
	}
	journal, _ := json.Marshal([]transactionRoot{{Root: windowPairingRoot, HadOriginal: true}})
	if err := os.WriteFile(journalPath(root), journal, 0600); err != nil {
		t.Fatal(err)
	}
	if err := RecoverInterrupted(root); err != nil {
		t.Fatal(err)
	}
	assertHostRecoveryFixture(t, root, fixture)
	if PendingRecovery(root) {
		t.Fatal("rollback journal remains")
	}
	if _, err := os.Stat(target + ".recovery-previous"); !os.IsNotExist(err) {
		t.Fatal("previous identity retained")
	}
}

func TestWindowPairingValidatorRejectsInjectedWhitespace(t *testing.T) {
	for _, suffix := range []string{"\n", "\r", "\x00", "\t"} {
		data := []byte(strings.TrimSuffix(string(syntheticWindowPairing("snapshot")), "\n") + suffix + "extra")
		if _, err := windowPairingFingerprint(data); err == nil {
			t.Fatal("accepted injected whitespace")
		}
	}
}

func TestWindowRecoveryUnitJournalIsScopedAndLegacyCompatible(t *testing.T) {
	for _, body := range []string{`{"scope":"window","units":["window.service"]}`, `{"scope":"window","units":[]}`} {
		units, scoped, err := decodeRecoveryUnits([]byte(body))
		if err != nil || !scoped || len(units) > 1 {
			t.Fatal("valid Window recovery journal rejected", err)
		}
	}
	units, scoped, err := decodeRecoveryUnits([]byte(`["neptune.service","gryphon.service","wyvern.service"]`))
	if err != nil || scoped || len(units) != 3 {
		t.Fatal("legacy journal rejected", err)
	}
	for _, body := range []string{`{"scope":"window","units":["neptune.service"]}`, `{"scope":"window","units":["window.service","window.service"]}`, `{"scope":"unknown","units":[]}`, `["sshd.service"]`, `invalid`, strings.Repeat(" ", 1025)} {
		if _, _, err := decodeRecoveryUnits([]byte(body)); err == nil {
			t.Fatal("invalid restart journal accepted", body)
		}
	}
}
