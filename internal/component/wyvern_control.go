package component

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var wyvernID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type WyvernProfile struct {
	Model           string   `json:"model"`
	Capabilities    []string `json:"capabilities"`
	MaxOutputTokens int      `json:"max_output_tokens"`
	Temperature     *float64 `json:"temperature,omitempty"`
	ThinkingBudget  *int     `json:"thinking_budget,omitempty"`
}
type WyvernAdapter struct {
	Name          string                   `json:"name"`
	Driver        string                   `json:"driver"`
	Enabled       bool                     `json:"enabled"`
	Profiles      map[string]WyvernProfile `json:"profiles"`
	CredentialRef string                   `json:"credential_ref,omitempty"`
	Endpoint      string                   `json:"endpoint,omitempty"`
	MaxConcurrent int                      `json:"max_concurrent,omitempty"`
	Timeout       int                      `json:"request_timeout_ms,omitempty"`
}
type WyvernBinding struct {
	AdapterID string `json:"adapter_id"`
	Profile   string `json:"profile"`
}
type WyvernClient struct {
	TokenHash    string                   `json:"token_sha256,omitempty"`
	Allowed      []string                 `json:"allowed_adapters"`
	Bindings     map[string]WyvernBinding `json:"bindings"`
	Requirements map[string][]string      `json:"requirements,omitempty"`
	Enabled      bool                     `json:"enabled"`
}
type WyvernConfiguration struct {
	Schema     string `json:"schema"`
	InstanceID string `json:"instance_id"`
	Revision   int    `json:"revision"`
	Config     struct {
		Schema     string                   `json:"schema"`
		InstanceID string                   `json:"instance_id"`
		Runtime    json.RawMessage          `json:"runtime,omitempty"`
		Adapters   map[string]WyvernAdapter `json:"adapters"`
		Clients    map[string]WyvernClient  `json:"clients"`
	} `json:"config"`
}
type wyvernManagerIdentity struct {
	Schema       string `json:"schema"`
	KernelURL    string `json:"kernel_url"`
	InstanceID   string `json:"instance_id"`
	Token        string `json:"manager_token"`
	RuntimeToken string `json:"runtime_token"`
}
type WyvernLink struct {
	Schema     string `json:"schema"`
	ClientID   string `json:"client_id"`
	InstanceID string `json:"instance_id"`
	Mode       string `json:"mode"`
	Socket     string `json:"socket,omitempty"`
	URL        string `json:"url,omitempty"`
	Token      string `json:"token"`
}

// Root is set only by isolated tests; production callers use fixed host paths.
type WyvernManager struct {
	Root    string
	Client  *http.Client
	Restart func(context.Context) error
}

func (m WyvernManager) path(value string) string {
	if m.Root == "" {
		return value
	}
	return filepath.Join(m.Root, strings.TrimPrefix(value, "/"))
}
func (m WyvernManager) identityPath() string { return m.path("/etc/exocortex/wyvern/manager.json") }
func WyvernLinkPath(head string) string {
	return "/etc/exocortex/wyvern/clients/" + head + "/link.json"
}
func hashToken(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func newWyvernToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}
func wyvernOrigin(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("Use the HTTPS Kernel origin without credentials or a path")
	}
	u.Path = ""
	return u.String(), nil
}
func (m WyvernManager) request(ctx context.Context, method, origin, route string, input any, headers map[string]string, output any) (*http.Response, error) {
	var body bytes.Buffer
	if input != nil {
		if err := json.NewEncoder(&body).Encode(input); err != nil {
			return nil, errors.New("Invalid Wyvern operation")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, origin+route, &body)
	if err != nil {
		return nil, errors.New("Invalid Kernel origin")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	client := m.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("Kernel connection unavailable; inspect the operation before retrying")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 524289))
	if err != nil || len(data) > 524288 {
		return nil, errors.New("Kernel response is invalid or interrupted")
	}
	if response.StatusCode == 409 {
		return nil, errors.New("Wyvern configuration changed; refresh before applying the change")
	}
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return nil, errors.New("Kernel rejected the connection identity or its permissions")
	}
	if response.StatusCode != 200 {
		return nil, errors.New("Kernel rejected the Wyvern operation; inspect configuration and permissions")
	}
	if output != nil && json.Unmarshal(data, output) != nil {
		return nil, errors.New("Kernel returned an invalid Wyvern response")
	}
	return response, nil
}
func atomicWyvernFile(filename string, value any, uid int) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWyvernBytes(filename, append(body, '\n'), uid)
}
func atomicWyvernBytes(filename string, body []byte, uid int) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(filename), ".wyvern-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(body)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if uid >= 0 {
		if err := os.Chown(temporary, uid, uid); err != nil {
			return err
		}
	}
	if err := os.Rename(temporary, filename); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(filename))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (m WyvernManager) identity() (wyvernManagerIdentity, error) {
	var identity wyvernManagerIdentity
	info, err := os.Lstat(m.identityPath())
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return identity, errors.New("Connect Wyvern to Kernel in the host TUI first")
	}
	body, err := os.ReadFile(m.identityPath())
	if err != nil || json.Unmarshal(body, &identity) != nil {
		return identity, errors.New("Invalid Wyvern management identity")
	}
	if identity.Schema != "exocortex.wyvern.manager.v1" || !wyvernID.MatchString(identity.InstanceID) || len(identity.Token) != 64 {
		return identity, errors.New("Invalid Wyvern management identity")
	}
	if _, err := wyvernOrigin(identity.KernelURL); err != nil {
		return identity, err
	}
	return identity, nil
}
func (m WyvernManager) Connect(ctx context.Context, origin, accessKey, instance string) error {
	origin, err := wyvernOrigin(origin)
	if err != nil {
		return err
	}
	if !wyvernID.MatchString(instance) || accessKey == "" {
		return errors.New("Kernel Access Key and a valid host instance ID are required")
	}
	var signedIn struct {
		Authenticated bool `json:"authenticated"`
	}
	response, err := m.request(ctx, "POST", origin, "/api/auth/login", map[string]string{"access_key": accessKey}, nil, &signedIn)
	if err != nil {
		return err
	}
	if !signedIn.Authenticated {
		return errors.New("Kernel authentication was not verified")
	}
	cookie := ""
	for _, candidate := range response.Cookies() {
		if candidate.Name == "kernel_session" {
			cookie = candidate.Name + "=" + candidate.Value
		}
	}
	if cookie == "" {
		return errors.New("Kernel did not issue an operator session")
	}
	defer func() {
		_, _ = m.request(context.Background(), "POST", origin, "/api/auth/logout", nil, map[string]string{"Cookie": cookie}, nil)
	}()
	// Reconnecting an existing identity is idempotent. Changing hosts/instances
	// needs an explicit migration so existing consumer tokens are not stranded.
	existing, existingErr := m.identity()
	if existingErr == nil && (existing.KernelURL != origin || existing.InstanceID != instance) {
		return errors.New("Wyvern is already connected to another Kernel instance")
	}
	wasDraining := false
	if m.Root == "" && m.Restart == nil {
		d := WyvernDeployment{}
		view, err := d.read(ctx)
		if err != nil {
			return errors.New("Install or repair Wyvern before connecting Kernel")
		}
		wasDraining = view.Status.Drain
		if _, err := d.control(ctx, "drain"); err != nil {
			return err
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if !wasDraining {
				_, _ = d.control(cleanup, "resume")
			}
		}()
		deadline := time.Now().Add(30 * time.Second)
		for view.Status.ActiveRequests > 0 {
			if time.Now().After(deadline) {
				return errors.New("Active requests did not drain; Kernel enrollment was not changed")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			view, err = d.read(ctx)
			if err != nil {
				return err
			}
		}
	}
	identity := wyvernManagerIdentity{Schema: "exocortex.wyvern.manager.v1", KernelURL: origin, InstanceID: instance}
	identity.Token, err = newWyvernToken()
	if err != nil {
		return err
	}
	identity.RuntimeToken, err = newWyvernToken()
	if err != nil {
		return err
	}
	if existingErr == nil {
		identity = existing
	}
	// A durable pending identity survives a lost enrollment response; no Access
	// Key or operator session is written to disk.
	pending := m.identityPath() + ".pending"
	if body, readErr := os.ReadFile(pending); readErr == nil {
		var prior wyvernManagerIdentity
		if json.Unmarshal(body, &prior) == nil && prior.KernelURL == origin && prior.InstanceID == instance && len(prior.Token) == 64 && len(prior.RuntimeToken) == 64 {
			identity = prior
		}
	}
	if err := atomicWyvernFile(pending, identity, -1); err != nil {
		return err
	}
	var result struct {
		Schema     string `json:"schema"`
		ConfigKey  string `json:"config_key"`
		InstanceID string `json:"instance_id"`
	}
	_, err = m.request(ctx, "POST", origin, "/api/wyvern/instances/"+instance+"/enroll", map[string]string{"manager_token_sha256": hashToken(identity.Token), "runtime_token_sha256": hashToken(identity.RuntimeToken)}, map[string]string{"Cookie": cookie}, &result)
	if err != nil {
		return err
	}
	if result.Schema != "exocortex.kernel.wyvern.enrollment.v1" || result.InstanceID != instance || result.ConfigKey != "wyvern.instances."+instance+".config" {
		return errors.New("Kernel returned an incompatible enrollment")
	}
	uid := 10001
	if m.Root != "" {
		uid = -1
	}
	if err := atomicWyvernBytes(m.path("/etc/wyvern/identity/kernel.token"), []byte(identity.RuntimeToken), uid); err != nil {
		return err
	}
	if err := atomicWyvernFile(m.path("/etc/wyvern/identity/bootstrap.json"), map[string]string{"kernel_origin": origin, "kernel_credential_file": "/etc/wyvern/identity/kernel.token", "config_key": result.ConfigKey}, uid); err != nil {
		return err
	}
	if m.Root == "" {
		if err := os.Chown("/etc/wyvern/identity", 10001, 10001); err != nil {
			return err
		}
		if err := os.Chmod("/etc/wyvern", 0755); err != nil {
			return err
		}
	}
	if err := os.Rename(pending, m.identityPath()); err != nil {
		return err
	}
	if m.Restart != nil {
		return m.Restart(ctx)
	}
	if m.Root == "" {
		if err := atomicWyvernBytes(wyvernMaintenance, []byte("maintenance\n"), -1); err != nil {
			return err
		}
		if err := exec.CommandContext(ctx, "systemctl", "restart", "wyvern.service").Run(); err != nil {
			return errors.New("Enrollment saved; Wyvern restart failed, inspect its installed unit")
		}
		for {
			view, err := (WyvernDeployment{}).read(ctx)
			if err == nil && view.Status.ConfigurationLoaded {
				break
			}
			select {
			case <-ctx.Done():
				return errors.New("Enrollment saved; runtime configuration requires repair")
			case <-time.After(200 * time.Millisecond):
			}
		}
		if err := os.Remove(wyvernMaintenance); err != nil {
			return err
		}
		if wasDraining {
			_, err := (WyvernDeployment{}).control(ctx, "drain")
			return err
		}
	}
	return nil
}
func (m WyvernManager) Configuration(ctx context.Context) (WyvernConfiguration, error) {
	var config WyvernConfiguration
	identity, err := m.identity()
	if err != nil {
		return config, err
	}
	_, err = m.request(ctx, "GET", identity.KernelURL, "/api/v1/wyvern/"+identity.InstanceID, nil, map[string]string{"Authorization": "Bearer " + identity.Token}, &config)
	if err == nil && (config.Schema != "exocortex.kernel.wyvern.v1" || config.InstanceID != identity.InstanceID || config.Revision < 1 || len(config.Config.Adapters) > 16 || len(config.Config.Clients) > 256) {
		err = errors.New("Kernel returned an incompatible Wyvern configuration")
	}
	return config, err
}

func (m WyvernManager) ReadLink(headID string) (WyvernLink, error) {
	var link WyvernLink
	if !wyvernID.MatchString(headID) {
		return link, errors.New("Invalid client identity")
	}
	filename := m.path(WyvernLinkPath(headID))
	info, err := os.Lstat(filename)
	if err != nil {
		return link, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 || info.Mode().Perm()&0007 != 0 {
		return link, errors.New("Invalid protected Wyvern client link")
	}
	body, err := os.ReadFile(filename)
	if err != nil {
		return link, err
	}
	if len(body) > 4096 || json.Unmarshal(body, &link) != nil {
		return link, errors.New("Invalid Wyvern client link")
	}
	return link, validateWyvernLink(link, headID)
}
func validateWyvernLink(link WyvernLink, headID string) error {
	if link.Schema != "exocortex.wyvern.link.v1" || link.ClientID != headID || !wyvernID.MatchString(link.InstanceID) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(link.Token) {
		return errors.New("Invalid Wyvern client identity")
	}
	if link.Mode == "local" && link.Socket == "/run/wyvern/client.sock" && link.URL == "" {
		return nil
	}
	u, err := url.Parse(link.URL)
	if link.Mode != "remote" || link.Socket != "" || err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || strings.Contains(u.Path, "..") {
		return errors.New("Remote Wyvern requires an HTTPS URL without credentials")
	}
	return nil
}

// Import is an explicit root operation. Verify caller and instance before
// replacing the local link; an unavailable remote never selects local mode.
func (m WyvernManager) ImportLink(ctx context.Context, headID string, link WyvernLink) error {
	if err := validateWyvernLink(link, headID); err != nil {
		return err
	}
	if link.Mode != "remote" {
		return errors.New("Only remote links can be imported")
	}
	var status struct {
		Schema     string `json:"schema"`
		ClientID   string `json:"client_id"`
		InstanceID string `json:"instance_id"`
	}
	if _, err := m.request(ctx, "GET", strings.TrimRight(link.URL, "/"), "/v1/client", nil, map[string]string{"Authorization": "Bearer " + link.Token}, &status); err != nil {
		return err
	}
	if status.Schema != "exocortex.wyvern.client.v1" || status.ClientID != headID || status.InstanceID != link.InstanceID {
		return errors.New("Remote Wyvern identity does not match the imported link")
	}
	uid := 10001
	if m.Root != "" {
		uid = -1
	}
	return m.writeLink(headID, link, uid)
}

// Export issues one scoped identity for an explicitly selected remote consumer.
// The root-only transfer file is never returned through API jobs or terminal output.
func (m WyvernManager) ExportLink(ctx context.Context, clientID, service, origin, filename, requestID string) error {
	if !filepath.IsAbs(filename) {
		return errors.New("Use an absolute path for the protected link file")
	}
	if _, err := os.Lstat(filename); !os.IsNotExist(err) {
		return errors.New("Export destination already exists or is unavailable")
	}
	if WyvernRequirements(service) == nil || !wyvernID.MatchString(clientID) {
		return errors.New("Invalid remote consumer")
	}
	probe := WyvernLink{Schema: "exocortex.wyvern.link.v1", ClientID: clientID, InstanceID: "probe", Mode: "remote", URL: origin, Token: strings.Repeat("a", 64)}
	if err := validateWyvernLink(probe, clientID); err != nil {
		return err
	}
	link, err := m.EnsureClient(ctx, clientID, service, requestID)
	if err != nil {
		return err
	}
	link.Mode, link.Socket, link.URL = "remote", "", strings.TrimRight(origin, "/")
	body, err := json.MarshalIndent(link, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err = file.Write(append(body, '\n')); err != nil {
		return err
	}
	return file.Sync()
}
func (m WyvernManager) Mutate(ctx context.Context, input map[string]any) (WyvernConfiguration, error) {
	var config WyvernConfiguration
	identity, err := m.identity()
	if err != nil {
		return config, err
	}
	_, err = m.request(ctx, "POST", identity.KernelURL, "/api/v1/wyvern/"+identity.InstanceID+"/mutations", input, map[string]string{"Authorization": "Bearer " + identity.Token}, &config)
	return config, err
}
func WyvernRequirements(service string) map[string][]string {
	switch service {
	case "mastermind":
		return map[string][]string{"text": {"text", "structured_output", "token_count"}, "media": {"text", "structured_output", "token_count", "image", "pdf", "audio", "video", "youtube"}}
	case "laboratory":
		return map[string][]string{"derivatives": {"text", "structured_output", "pdf"}}
	}
	return nil
}
func (m WyvernManager) EnsureClient(ctx context.Context, headID, service, requestID string) (WyvernLink, error) {
	var link WyvernLink
	if !wyvernID.MatchString(headID) || WyvernRequirements(service) == nil {
		return link, errors.New("Service does not consume Wyvern")
	}
	config, err := m.Configuration(ctx)
	if err != nil {
		return link, err
	}
	filename := m.path(WyvernLinkPath(headID))
	if body, err := os.ReadFile(filename); err == nil {
		if json.Unmarshal(body, &link) != nil || validateWyvernLink(link, headID) != nil || link.InstanceID != config.InstanceID {
			return link, errors.New("Existing client link is incompatible; explicit repair is required")
		}
		if client, ok := config.Config.Clients[headID]; ok {
			if !client.Enabled || client.TokenHash != hashToken(link.Token) {
				return link, errors.New("Wyvern client was revoked or rotated; explicit repair is required")
			}
			return link, nil
		}
	} else if !os.IsNotExist(err) {
		return link, err
	}
	if link.Token == "" {
		token, err := newWyvernToken()
		if err != nil {
			return link, err
		}
		link = WyvernLink{Schema: "exocortex.wyvern.link.v1", ClientID: headID, InstanceID: config.InstanceID, Mode: "local", Socket: "/run/wyvern/client.sock", Token: token}
	}
	if _, exists := config.Config.Clients[headID]; exists {
		return link, errors.New("Client is registered but its local token is missing; repair instead of overwriting it")
	}
	uid := 10001
	if m.Root != "" {
		uid = -1
	}
	if err := m.writeLink(headID, link, uid); err != nil {
		return link, err
	}
	allowed := []string{}
	for id, adapter := range config.Config.Adapters {
		if adapter.Enabled {
			allowed = append(allowed, id)
		}
	}
	_, err = m.Mutate(ctx, map[string]any{"operation": "client.put", "expected_revision": config.Revision, "request_id": requestID, "client_id": headID,
		"client": WyvernClient{TokenHash: hashToken(link.Token), Allowed: allowed, Bindings: map[string]WyvernBinding{}, Requirements: WyvernRequirements(service), Enabled: true}})
	return link, err
}

func (m WyvernManager) writeLink(headID string, link WyvernLink, uid int) error {
	filename := m.path(WyvernLinkPath(headID))
	if err := atomicWyvernFile(filename, link, uid); err != nil {
		return err
	}
	if uid >= 0 {
		if err := os.Chown(filepath.Dir(filename), uid, uid); err != nil {
			return err
		}
	}
	if err := os.Chmod(filename, 0640); err != nil {
		return err
	}
	return os.Chmod(filepath.Dir(filename), 0750)
}

func (m WyvernManager) RotateClient(ctx context.Context, clientID string) error {
	if !wyvernID.MatchString(clientID) {
		return errors.New("Invalid client identity")
	}
	config, err := m.Configuration(ctx)
	if err != nil {
		return err
	}
	client, exists := config.Config.Clients[clientID]
	if !exists {
		return errors.New("Client is not registered")
	}
	pending := m.path(WyvernLinkPath(clientID)) + ".pending"
	link := WyvernLink{Schema: "exocortex.wyvern.link.v1", ClientID: clientID, InstanceID: config.InstanceID, Mode: "local", Socket: "/run/wyvern/client.sock"}
	if prior, err := m.ReadLink(clientID); err == nil && prior.InstanceID == config.InstanceID {
		link = prior
	}
	if body, err := os.ReadFile(pending); err == nil {
		if json.Unmarshal(body, &link) != nil || validateWyvernLink(link, clientID) != nil || link.InstanceID != config.InstanceID {
			return errors.New("Invalid pending rotation")
		}
	} else if !os.IsNotExist(err) {
		return err
	} else {
		link.Token, err = newWyvernToken()
		if err != nil {
			return err
		}
		if err := atomicWyvernFile(pending, link, -1); err != nil {
			return err
		}
	}
	if client.TokenHash != hashToken(link.Token) {
		client.TokenHash = hashToken(link.Token)
		_, err = m.Mutate(ctx, map[string]any{"operation": "client.put", "client_id": clientID, "client": client, "expected_revision": config.Revision, "request_id": "rotate-" + client.TokenHash[:32]})
		if err != nil {
			return err
		}
	}
	uid := 10001
	if m.Root != "" {
		uid = -1
	}
	if err := m.writeLink(clientID, link, uid); err != nil {
		return err
	}
	return os.Remove(pending)
}
