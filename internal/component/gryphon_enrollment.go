package component

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The Core receives its own registration only, never the shared clients directory.
func enrollMastermindGryphon() error {
	group, err := user.LookupGroup("gryphon-clients")
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return err
	}
	token, err := provisionMastermindGryphon("/etc/gryphon/clients", "/etc/exocortex/gryphon/clients/mastermind", gid, 10001)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", gryphonSocket)
	}}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://gryphon.local/v1/service", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	remote, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return errors.New("Gryphon client verification is unavailable")
	}
	defer remote.Body.Close()
	var status struct {
		ServiceID string `json:"serviceId"`
		Schema    string `json:"schema"`
	}
	if remote.StatusCode != 200 || json.NewDecoder(io.LimitReader(remote.Body, 65536)).Decode(&status) != nil ||
		status.ServiceID != "mastermind" || status.Schema != "exocortex.gryphon.service-status.v1" {
		return errors.New("Gryphon did not verify the Mastermind client")
	}
	return nil
}

func provisionMastermindGryphon(clients, scoped string, gatewayGID, coreGID int) (string, error) {
	for directory, gid := range map[string]int{clients: gatewayGID, scoped: coreGID} {
		if err := os.MkdirAll(directory, 0750); err != nil {
			return "", err
		}
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("unsafe Gryphon credential directory")
		}
		if err := os.Chown(directory, 0, gid); err != nil {
			return "", err
		}
		if err := os.Chmod(directory, 0750); err != nil {
			return "", err
		}
	}
	registered := filepath.Join(clients, "mastermind.token")
	token := ""
	if info, err := os.Lstat(registered); err == nil {
		if !info.Mode().IsRegular() || info.Size() > 8192 {
			return "", errors.New("unsafe Gryphon credential")
		}
		value, err := os.ReadFile(registered)
		if err != nil {
			return "", err
		}
		token = strings.TrimRight(string(value), "\r\n")
		if !regexp.MustCompile(`^[A-Za-z0-9_-]{32,256}$`).MatchString(token) {
			return "", errors.New("invalid Gryphon client credential")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	} else {
		var err error
		token, err = randomToken()
		if err != nil {
			return "", err
		}
		if err := writeSecret(registered, token, gatewayGID); err != nil {
			return "", err
		}
	}
	// Atomic publication into the mounted directory is visible without recreating Core.
	if err := writeSecret(filepath.Join(scoped, "mastermind.token"), token, coreGID); err != nil {
		return "", err
	}
	return token, nil
}
