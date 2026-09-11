package component

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"updater/internal/config"
	"updater/internal/kernel"
	"updater/internal/model"
	"updater/internal/state"
)

var neptuneInitializationLock sync.Mutex

func StartNeptuneInitialization(runtimeConfig config.Runtime, store *state.Store, request model.NeptuneInitializationRequest) (model.Job, error) {
	if request.RequestID == "" || request.HeadID == "" || request.ProjectID == "" || request.ExportURL == "" || request.EnrollmentCode == "" {
		return model.Job{}, errors.New("request_id, head_id, project_id, export_url and enrollment_code are required")
	}
	if previous, ok := store.ByRequestID(request.RequestID); ok {
		if previous.HeadID != request.HeadID || previous.Service != "neptune-initialization" {
			return model.Job{}, errors.New("request id is already in use")
		}
		return previous, nil
	}
	head, err := config.LoadHead(runtimeConfig, request.HeadID)
	if err != nil {
		return model.Job{}, err
	}
	if head.Service != request.ProjectID {
		return model.Job{}, errors.New("Neptune project must match the registered service")
	}
	if !safeNeptuneID.MatchString(request.ProjectID) || !regexpEnrollmentCode.MatchString(strings.TrimSpace(request.EnrollmentCode)) {
		return model.Job{}, errors.New("Neptune initialization input is invalid")
	}
	parsedExport, parseErr := url.Parse(request.ExportURL)
	if parseErr != nil || parsedExport.Scheme != "http" || !parsedExport.IsAbs() {
		return model.Job{}, errors.New("backup export URL must be an absolute loopback HTTP URL")
	}
	exportIP := net.ParseIP(parsedExport.Hostname())
	if parsedExport.Hostname() != "localhost" && (exportIP == nil || !exportIP.IsLoopback()) {
		return model.Job{}, errors.New("backup export URL must be loopback-only")
	}
	releaseOperation, err := store.BeginOperation("")
	if err != nil {
		return model.Job{}, err
	}
	if !neptuneInitializationLock.TryLock() {
		releaseOperation()
		return model.Job{}, errors.New("another Neptune initialization is already running")
	}
	now := time.Now().UTC()
	digest := sha256.Sum256([]byte(request.RequestID))
	job := model.Job{
		ID: fmt.Sprintf("neptune-%d-%x", now.Unix(), digest[:8]), RequestID: request.RequestID,
		HeadID: request.HeadID, Service: "neptune-initialization", State: "REQUESTED",
		Message: "Neptune initialization accepted", CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Save(job); err != nil {
		neptuneInitializationLock.Unlock()
		releaseOperation()
		return model.Job{}, err
	}
	go func(job model.Job) {
		defer releaseOperation()
		defer neptuneInitializationLock.Unlock()
		update := func(stateName, message string, finished bool) {
			job.State, job.Message, job.UpdatedAt = stateName, message, time.Now().UTC()
			if finished {
				value := job.UpdatedAt
				job.FinishedAt = &value
			}
			_ = store.Save(job)
		}
		update("INSTALLING", "Installing or verifying Neptune Linux", false)
		version, installErr := InstallLatestNeptune(runtimeConfig, request.HeadID)
		if installErr != nil {
			update("FAILED", installErr.Error(), true)
			return
		}
		job.Version = version
		update("ENROLLING", "Linking this service to Saturn backup storage", false)
		if _, enrollErr := EnrollNeptuneProject(runtimeConfig, request.HeadID, request.ProjectID, request.ExportURL, request.EnrollmentCode); enrollErr != nil {
			update("FAILED", enrollErr.Error(), true)
			return
		}
		update("COMPLETED", "Neptune initialized and linked", true)
	}(job)
	return job, nil
}

type NeptuneEnrollmentResult struct {
	ProjectID     string `json:"project_id"`
	ProducerSlug  string `json:"producer_slug"`
	NamespaceSlug string `json:"namespace_slug"`
	DeploymentID  string `json:"deployment_id"`
	SocketGID     int    `json:"socket_gid"`
	MirrorRoot    string `json:"mirror_root,omitempty"`
}

type saturnEnrollment struct {
	Token                string `json:"token"`
	Slug                 string `json:"slug"`
	NamespaceSlug        string `json:"namespaceSlug"`
	DeploymentID         string `json:"deploymentId"`
	MirrorRoot           string `json:"mirrorRoot"`
	MirrorToken          string `json:"mirrorToken"`
	MirrorMode           string `json:"mirrorMode"`
	MirrorTargetFilename string `json:"mirrorTargetFilename"`
}

func EnrollNeptuneProject(runtimeConfig config.Runtime, headID, projectID, exportURL, code string) (NeptuneEnrollmentResult, error) {
	if !safeNeptuneID.MatchString(projectID) {
		return NeptuneEnrollmentResult{}, errors.New("project ID must use lowercase letters, numbers and hyphens")
	}
	parsedExport, err := url.Parse(exportURL)
	if err != nil || parsedExport.Scheme != "http" || (parsedExport.Hostname() != "localhost" && net.ParseIP(parsedExport.Hostname()) == nil) || !parsedExport.IsAbs() {
		return NeptuneEnrollmentResult{}, errors.New("backup export URL must be an absolute loopback HTTP URL")
	}
	ip := net.ParseIP(parsedExport.Hostname())
	if ip != nil && !ip.IsLoopback() {
		return NeptuneEnrollmentResult{}, errors.New("backup export URL must be loopback-only")
	}
	if !regexpEnrollmentCode.MatchString(strings.TrimSpace(code)) {
		return NeptuneEnrollmentResult{}, errors.New("Saturn setup code is invalid")
	}
	head, err := config.LoadHead(runtimeConfig, headID)
	if err != nil {
		return NeptuneEnrollmentResult{}, err
	}
	snapshot, err := kernel.Load(head.KernelURL, head.KernelServiceToken, head.KernelCachePath, 5*time.Second)
	if err != nil {
		return NeptuneEnrollmentResult{}, err
	}
	host, err := kernel.String(snapshot, "services.saturn.sni")
	if err != nil {
		return NeptuneEnrollmentResult{}, err
	}
	port, err := kernel.String(snapshot, "services.saturn.port")
	if err != nil {
		return NeptuneEnrollmentResult{}, err
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return NeptuneEnrollmentResult{}, errors.New("services.saturn.port is invalid")
	}
	origin := "https://" + net.JoinHostPort(host, port)
	if port == "443" {
		origin = "https://" + host
	}
	redeemed, err := redeemSaturnEnrollment(origin, strings.TrimSpace(code))
	if err != nil {
		return NeptuneEnrollmentResult{}, err
	}
	if err := validateNeptuneEnrollmentProfile(projectID, redeemed); err != nil {
		return NeptuneEnrollmentResult{}, err
	}
	if redeemed.MirrorRoot != "" {
		validRoot := redeemed.MirrorRoot == "volt" || redeemed.MirrorRoot == "mastermind"
		validMode := redeemed.MirrorMode == "single-file" || redeemed.MirrorMode == "zip-tree"
		if !validRoot || !validMode || redeemed.MirrorToken == "" || !strings.HasSuffix(exportURL, "/backup") {
			return NeptuneEnrollmentResult{}, errors.New("Saturn returned an incomplete mirror enrollment")
		}
	}

	group, err := user.LookupGroup("neptune-clients")
	if err != nil {
		return NeptuneEnrollmentResult{}, errors.New("neptune-clients group is unavailable; install Neptune first")
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return NeptuneEnrollmentResult{}, err
	}
	clientsDir := "/etc/neptune/clients"
	projectsDir := "/etc/neptune/projects"
	if err := os.MkdirAll(clientsDir, 0o750); err != nil {
		return NeptuneEnrollmentResult{}, err
	}
	if err := os.MkdirAll(projectsDir, 0o750); err != nil {
		return NeptuneEnrollmentResult{}, err
	}
	controlToken, err := randomToken()
	if err != nil {
		return NeptuneEnrollmentResult{}, err
	}
	exportToken, err := randomToken()
	if err != nil {
		return NeptuneEnrollmentResult{}, err
	}
	controlPath := filepath.Join(clientsDir, projectID+".control.token")
	exportPath := filepath.Join(clientsDir, projectID+".export.token")
	saturnPath := filepath.Join(clientsDir, projectID+".saturn.token")
	mirrorPath := filepath.Join(clientsDir, projectID+".mirror.token")
	secrets := map[string]string{controlPath: controlToken, exportPath: exportToken, saturnPath: redeemed.Token}
	if redeemed.MirrorToken != "" {
		secrets[mirrorPath] = redeemed.MirrorToken
	}
	for path, value := range secrets {
		if err := writeSecret(path, value, gid); err != nil {
			return NeptuneEnrollmentResult{}, err
		}
	}
	projectEnv := filepath.Join(projectsDir, projectID+".env")
	content := fmt.Sprintf("NEPTUNE_BACKUP_EXPORT_URL=%s\nNEPTUNE_CONTROL_TOKEN_FILE=%s\nNEPTUNE_EXPORT_TOKEN_FILE=%s\nNEPTUNE_SATURN_TOKEN_FILE=%s\nNEPTUNE_SATURN_SLUG=%s\nNEPTUNE_BACKUP_ENABLED=false\nNEPTUNE_BACKUP_INTERVAL_HOURS=24\n", exportURL, controlPath, exportPath, saturnPath, redeemed.Slug)
	if redeemed.MirrorRoot != "" {
		content += fmt.Sprintf("NEPTUNE_MIRROR_ROOT=%s\nNEPTUNE_MIRROR_TOKEN_FILE=%s\nNEPTUNE_MIRROR_EXPORT_URL=%s\nNEPTUNE_MIRROR_MODE=%s\nNEPTUNE_MIRROR_TARGET_FILENAME=%s\nNEPTUNE_MIRROR_ENABLED=false\nNEPTUNE_MIRROR_INTERVAL_MINUTES=5\n", redeemed.MirrorRoot, mirrorPath, strings.TrimSuffix(exportURL, "/backup")+"/mirror", redeemed.MirrorMode, redeemed.MirrorTargetFilename)
	}
	if err := os.WriteFile(projectEnv+".tmp", []byte(content), 0o640); err != nil {
		return NeptuneEnrollmentResult{}, err
	}
	if err := os.Chown(projectEnv+".tmp", 0, gid); err != nil {
		return NeptuneEnrollmentResult{}, err
	}
	if err := os.Rename(projectEnv+".tmp", projectEnv); err != nil {
		return NeptuneEnrollmentResult{}, err
	}
	serviceAccount, err := user.Lookup("neptune")
	if err != nil {
		return NeptuneEnrollmentResult{}, errors.New("Neptune service account is unavailable; install Neptune first")
	}
	uid, uidErr := strconv.Atoi(serviceAccount.Uid)
	primaryGID, primaryGIDErr := strconv.Atoi(serviceAccount.Gid)
	if uidErr != nil || primaryGIDErr != nil {
		return NeptuneEnrollmentResult{}, errors.New("Neptune service account IDs are invalid")
	}
	registration := exec.Command(neptuneBinary, "register-project", projectID, projectEnv)
	registration.Env = append(os.Environ(), "HOME=/var/lib/neptune", "DOTNET_BUNDLE_EXTRACT_BASE_DIR=/var/cache/neptune")
	registration.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
		Uid: uint32(uid), Gid: uint32(primaryGID), Groups: []uint32{uint32(gid)},
	}}
	if output, commandErr := registration.CombinedOutput(); commandErr != nil {
		return NeptuneEnrollmentResult{}, fmt.Errorf("Neptune project registration failed: %s", strings.TrimSpace(string(output)))
	}
	if err := updateEnvFile(head.EnvFile, map[string]string{
		"NEPTUNE_PROJECT_ID": projectID, "NEPTUNE_SOCKET_DIR": "/run/neptune", "NEPTUNE_SOCKET_GID": strconv.Itoa(gid),
		"NEPTUNE_CONTROL_TOKEN_HOST_FILE": controlPath, "NEPTUNE_EXPORT_TOKEN_HOST_FILE": exportPath,
	}); err != nil {
		return NeptuneEnrollmentResult{}, err
	}
	command := exec.Command("docker", "compose", "--env-file", head.EnvFile, "-f", head.ComposeFile, "up", "-d", head.ComposeService)
	command.Dir = head.ProjectDir
	if output, commandErr := command.CombinedOutput(); commandErr != nil {
		return NeptuneEnrollmentResult{}, fmt.Errorf("service restart after Neptune enrollment failed: %s", strings.TrimSpace(string(output)))
	}
	healthContext, cancelHealth := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelHealth()
	if err := waitNeptuneProject(healthContext, projectID, controlToken); err != nil {
		return NeptuneEnrollmentResult{}, err
	}
	return NeptuneEnrollmentResult{ProjectID: projectID, ProducerSlug: redeemed.Slug, NamespaceSlug: redeemed.NamespaceSlug, DeploymentID: redeemed.DeploymentID, SocketGID: gid, MirrorRoot: redeemed.MirrorRoot}, nil
}

func waitNeptuneProject(ctx context.Context, projectID, token string) error {
	for {
		if err := checkNeptuneProject(ctx, projectID, token); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("Neptune project did not become ready before the initialization deadline")
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func checkNeptuneProject(ctx context.Context, projectID, token string) error {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", neptuneSocket)
	}}
	defer transport.CloseIdleConnections()
	route := "/v1/health"
	if projectID != "" {
		route = "/v1/projects/" + url.PathEscape(projectID) + "/status"
	}
	request, _ := http.NewRequestWithContext(ctx, "GET", "http://neptune.local"+route, nil)
	if token != "" {
		request.Header.Set("X-Neptune-Token", token)
	}
	response, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return errors.New("Neptune has not become reachable after initialization")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("Neptune project authentication or readiness failed")
	}
	return nil
}

func validateNeptuneEnrollmentProfile(projectID string, redeemed saturnEnrollment) error {
	if redeemed.NamespaceSlug != projectID {
		return fmt.Errorf("Saturn setup code belongs to namespace %q, expected %q", redeemed.NamespaceSlug, projectID)
	}
	if redeemed.MirrorRoot != "" && redeemed.MirrorRoot != projectID {
		return fmt.Errorf("Saturn setup code contains mirror root %q, expected %q", redeemed.MirrorRoot, projectID)
	}
	if projectID == "volt" {
		if redeemed.MirrorRoot != "volt" || redeemed.MirrorMode != "single-file" || redeemed.MirrorTargetFilename != "personal.volt" || redeemed.MirrorToken == "" {
			return errors.New("Volt requires a 'Volt ZIP + personal.volt mirror' setup code")
		}
	}
	return nil
}

var safeNeptuneID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var regexpEnrollmentCode = regexp.MustCompile(`^[A-Za-z0-9_-]{32}$`)

func redeemSaturnEnrollment(origin, code string) (saturnEnrollment, error) {
	body, _ := json.Marshal(map[string]string{"code": code})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(origin, "/")+"/api/v1/backup-enrollments/redeem", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "exocortex-updater")
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return saturnEnrollment{}, err
	}
	defer response.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
	if readErr != nil {
		return saturnEnrollment{}, readErr
	}
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return saturnEnrollment{}, fmt.Errorf("Saturn enrollment returned HTTP %d", response.StatusCode)
	}
	var result saturnEnrollment
	if json.Unmarshal(payload, &result) != nil || result.Token == "" || result.Slug == "" {
		return saturnEnrollment{}, errors.New("Saturn returned an invalid enrollment response")
	}
	return result, nil
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
func writeSecret(path, value string, gid int) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(value+"\n"), 0o640); err != nil {
		return err
	}
	if err := os.Chown(temporary, 0, gid); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func updateEnvFile(path string, replacements map[string]string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	for index, line := range lines {
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if value, exists := replacements[key]; exists {
			lines[index] = key + "=" + value
			seen[key] = true
		}
	}
	for key, value := range replacements {
		if !seen[key] {
			lines = append(lines, key+"="+value)
		}
	}
	temporary := path + ".neptune.tmp"
	if err := os.WriteFile(temporary, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
