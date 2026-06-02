package main

// Tiny typed wrapper around the myserver HTTP API. Only the endpoints the
// CLI uses live here — keep the surface narrow on purpose.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

type apiClient struct {
	url    string
	token  string
	teamID int64
	hc     *http.Client
}

func newAPI(creds *Credentials, teamID int64) *apiClient {
	return &apiClient{
		url:    strings.TrimRight(creds.APIURL, "/"),
		token:  creds.Token,
		teamID: teamID,
		hc:     &http.Client{Timeout: 30 * time.Second},
	}
}

func (a *apiClient) do(method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode body: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, a.url+path, rdr)
	if err != nil {
		return err
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	if a.teamID > 0 {
		req.Header.Set("X-Team-ID", fmt.Sprintf("%d", a.teamID))
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		// Attempt to surface a clean error message from {"error": "..."}.
		var errPayload struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(respBody, &errPayload) == nil && errPayload.Error != "" {
			return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, errPayload.Error)
		}
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response from %s: %w", path, err)
		}
	}
	return nil
}

// ── Auth ──────────────────────────────────────────────────────────────────

type loginResp struct {
	Tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresAt    int64  `json:"expires_at"`
	} `json:"tokens"`
	User struct {
		ID    int64  `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	} `json:"user"`
}

func apiLogin(apiURL, email, password string) (*loginResp, error) {
	c := &apiClient{url: strings.TrimRight(apiURL, "/"), hc: &http.Client{Timeout: 15 * time.Second}}
	var r loginResp
	if err := c.do("POST", "/api/v1/auth/login",
		map[string]string{"email": email, "password": password}, &r); err != nil {
		return nil, err
	}
	if r.Tokens.AccessToken == "" {
		return nil, fmt.Errorf("login: server returned no access token")
	}
	return &r, nil
}

// ── Device authorization grant (RFC 8628) ─────────────────────────────────

type deviceCodeResp struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type deviceTokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
}

// apiRequestDeviceCode mints a fresh (device_code, user_code) pair.
func apiRequestDeviceCode(apiURL, clientName string) (*deviceCodeResp, error) {
	c := &apiClient{url: strings.TrimRight(apiURL, "/"), hc: &http.Client{Timeout: 15 * time.Second}}
	var r deviceCodeResp
	if err := c.do("POST", "/api/v1/auth/device/code",
		map[string]string{"client_name": clientName}, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// apiPollDeviceToken polls once. Returns:
//   - (tokens, "", nil)   on success
//   - (nil, errCode, nil) on RFC 8628 errors (authorization_pending, slow_down,
//     access_denied, expired_token, invalid_grant)
//   - (nil, "", err)      on transport / unexpected error
func apiPollDeviceToken(apiURL, deviceCode string) (*deviceTokenResp, string, error) {
	body, err := json.Marshal(map[string]string{"device_code": deviceCode})
	if err != nil {
		return nil, "", err
	}
	url := strings.TrimRight(apiURL, "/") + "/api/v1/auth/device/token"
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	hc := &http.Client{Timeout: 15 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 200 {
		var r deviceTokenResp
		if err := json.Unmarshal(respBody, &r); err != nil {
			return nil, "", fmt.Errorf("decode device token: %w", err)
		}
		return &r, "", nil
	}
	// 4xx: parse RFC 8628 error envelope.
	var errPayload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(respBody, &errPayload) == nil && errPayload.Error != "" {
		return nil, errPayload.Error, nil
	}
	return nil, "", fmt.Errorf("device token poll: %d %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
}

// ── Servers / Apps / Projects ────────────────────────────────────────────

type Server struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	IP     string `json:"ip"`
	User   string `json:"user"`
	Port   int    `json:"port"`
	TeamID int64  `json:"team_id"`
}

// Team — minimal shape used by `myserver init`. Matches Teams.List response.
type Team struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type Application struct {
	ID                      int64  `json:"id"`
	Name                    string `json:"name"`
	BuildPack               string `json:"build_pack"`
	PortsExposes            string `json:"ports_exposes"`
	FQDN                    string `json:"fqdn"`
	Status                  string `json:"status"`
	EnvironmentID           int64  `json:"environment_id"`
	DestinationID           *int64 `json:"destination_id"`
	DockerRegistryImageName string `json:"docker_registry_image_name"`
	DockerRegistryImageTag  string `json:"docker_registry_image_tag"`
	DockerRegistryID        *int64 `json:"docker_registry_id"`
}

type DockerRegistry struct {
	ID          int64  `json:"id"`
	TeamID      int64  `json:"team_id"`
	Name        string `json:"name"`
	RegistryURL string `json:"registry_url"`
	Username    string `json:"username"`
	IsSystem    bool   `json:"is_system"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type CreateDockerRegistryRequest struct {
	Name        string `json:"name"`
	RegistryURL string `json:"registry_url"`
	Username    string `json:"username"`
	Password    string `json:"password"`
}

type UpdateDockerRegistryRequest struct {
	Name        *string `json:"name,omitempty"`
	RegistryURL *string `json:"registry_url,omitempty"`
	Username    *string `json:"username,omitempty"`
	Password    *string `json:"password,omitempty"`
}

type Deployment struct {
	ID             int64  `json:"id"`
	ApplicationID  int64  `json:"application_id"`
	Status         string `json:"status"`
	Error          string `json:"error"`
	DeploymentUUID string `json:"deployment_uuid"`
	CreatedAt      string `json:"created_at"`
}

type DeployLog struct {
	Src     string `json:"src"`
	Msg     string `json:"msg"`
	Command string `json:"command"`
	Order   int    `json:"order"`
	TS      string `json:"ts"`
	Hidden  bool   `json:"hidden"`
}

// PrivateKey is the shape returned by /api/v1/private-keys/* —
// minimal here, since the CLI only needs the id after creation.
type PrivateKey struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type CreatePrivateKeyRequest struct {
	Name         string `json:"name"`
	PrivateKey   string `json:"private_key"`
	Description  string `json:"description,omitempty"`
	IsGitRelated bool   `json:"is_git_related"`
}

func (a *apiClient) createPrivateKey(req CreatePrivateKeyRequest) (*PrivateKey, error) {
	var k PrivateKey
	if err := a.do("POST", "/api/v1/private-keys/", req, &k); err != nil {
		return nil, err
	}
	return &k, nil
}

type CreateServerRequest struct {
	Name         string  `json:"name"`
	IP           string  `json:"ip"`
	User         string  `json:"user"`
	Port         int     `json:"port"`
	PrivateKeyID int64   `json:"private_key_id"`
	Description  *string `json:"description,omitempty"`
}

func (a *apiClient) createServer(req CreateServerRequest) (*Server, error) {
	var s Server
	if err := a.do("POST", "/api/v1/servers/", req, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// deleteServer soft-deletes a server. Caveats handled server-side:
// refuses if any apps or databases are still assigned to it (use the
// /servers/{id} GET to see resource counts first, or remove the
// resources before deleting the server).
func (a *apiClient) deleteServer(id int64) error {
	return a.do("DELETE", fmt.Sprintf("/api/v1/servers/%d", id), nil, nil)
}

// SystemBackup mirrors the server-side shape for the CLI's list/get.
// Only the fields the CLI displays — full struct in
// internal/domain/system_backup.go.
type SystemBackup struct {
	ID            int64   `json:"id"`
	UUID          string  `json:"uuid"`
	TeamID        int64   `json:"team_id"`
	Status        string  `json:"status"`
	S3StorageID   *int64  `json:"s3_storage_id,omitempty"`
	LocalDiskPath string  `json:"local_disk_path,omitempty"`
	Filename      *string `json:"filename,omitempty"`
	Size          *int64  `json:"size,omitempty"`
	CreatedAt     string  `json:"created_at"`
}

type CreateSystemBackupRequest struct {
	S3StorageID   int64  `json:"s3_storage_id,omitempty"`
	LocalDiskPath string `json:"local_disk_path,omitempty"`
	IncludeDBData *bool  `json:"include_db_data,omitempty"`
}

func (a *apiClient) listSystemBackups() ([]SystemBackup, error) {
	var out []SystemBackup
	if err := a.do("GET", "/api/v1/system-backups", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *apiClient) createSystemBackup(req CreateSystemBackupRequest) (*SystemBackup, error) {
	var b SystemBackup
	if err := a.do("POST", "/api/v1/system-backups", req, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// downloadSystemBackup streams /system-backups/{id}/download/{filename}
// directly to the writer (no buffering — backups can be large). Server
// handles both S3 and local modes transparently.
func (a *apiClient) downloadSystemBackup(id int64, filename string, w io.Writer) error {
	req, err := http.NewRequest("GET", a.url+fmt.Sprintf("/api/v1/system-backups/%d/download/%s", id, filename), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	if a.teamID > 0 {
		req.Header.Set("X-Team-ID", fmt.Sprintf("%d", a.teamID))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("download %d %s: HTTP %d: %s", id, filename, resp.StatusCode, string(body))
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// validateServer triggers a server-side SSH probe to verify the new
// server is reachable + the SSH key works. Returns the validation
// result; the CLI surfaces it as part of `server register` so a
// misconfigured key fails fast instead of much later during a deploy.
func (a *apiClient) validateServer(id int64) error {
	return a.do("POST", fmt.Sprintf("/api/v1/servers/%d/validate", id), map[string]any{}, nil)
}

func (a *apiClient) listServers() ([]Server, error) {
	var s []Server
	if err := a.do("GET", "/api/v1/servers", nil, &s); err != nil {
		return nil, err
	}
	return s, nil
}

// listTeams returns every team the caller is a member of. Auth-only —
// team context (X-Team-ID) is ignored on this endpoint.
func (a *apiClient) listTeams() ([]Team, error) {
	var t []Team
	if err := a.do("GET", "/api/v1/teams", nil, &t); err != nil {
		return nil, err
	}
	return t, nil
}

// listApps lists every application visible to the caller within the
// configured team (X-Team-ID header on the apiClient).
func (a *apiClient) listApps() ([]Application, error) {
	var apps []Application
	if err := a.do("GET", "/api/v1/applications/", nil, &apps); err != nil {
		return nil, err
	}
	return apps, nil
}

func (a *apiClient) listDockerRegistries() ([]DockerRegistry, error) {
	var regs []DockerRegistry
	if err := a.do("GET", "/api/v1/docker-registries/", nil, &regs); err != nil {
		return nil, err
	}
	return regs, nil
}

func (a *apiClient) createDockerRegistry(req CreateDockerRegistryRequest) (*DockerRegistry, error) {
	var reg DockerRegistry
	if err := a.do("POST", "/api/v1/docker-registries/", req, &reg); err != nil {
		return nil, err
	}
	return &reg, nil
}

func (a *apiClient) getDockerRegistry(id int64) (*DockerRegistry, error) {
	var reg DockerRegistry
	if err := a.do("GET", fmt.Sprintf("/api/v1/docker-registries/%d", id), nil, &reg); err != nil {
		return nil, err
	}
	return &reg, nil
}

func (a *apiClient) updateDockerRegistry(id int64, req UpdateDockerRegistryRequest) (*DockerRegistry, error) {
	var reg DockerRegistry
	if err := a.do("PATCH", fmt.Sprintf("/api/v1/docker-registries/%d", id), req, &reg); err != nil {
		return nil, err
	}
	return &reg, nil
}

func (a *apiClient) deleteDockerRegistry(id int64) error {
	return a.do("DELETE", fmt.Sprintf("/api/v1/docker-registries/%d", id), nil, nil)
}

// CreateApplicationRequest covers the common-path fields for `myserver app
// create`. The full server-side struct has ~50 fields (canary settings,
// resource limits, forward-auth, etc.); we expose the ones a customer or
// AI realistically wants to set on first creation. Power users can hit the
// API directly for the long tail.
type CreateApplicationRequest struct {
	Name          string `json:"name"`
	EnvironmentID int64  `json:"environment_id"`
	BuildPack     string `json:"build_pack"`

	Description   *string `json:"description,omitempty"`
	GitRepository *string `json:"git_repository,omitempty"`
	GitBranch     *string `json:"git_branch,omitempty"`
	FQDN          *string `json:"fqdn,omitempty"`
	PortsExposes  *string `json:"ports_exposes,omitempty"`

	// Server binding — which physical server the app deploys to. If unset,
	// the API picks the team's default server.
	ServerID *int64 `json:"server_id,omitempty"`

	// Build-pack specific. Only set the ones relevant to your build_pack.
	Dockerfile              *string `json:"dockerfile,omitempty"`
	DockerfileLocation      *string `json:"dockerfile_location,omitempty"`
	DockerComposeLocation   *string `json:"docker_compose_location,omitempty"`
	DockerComposeRaw        *string `json:"docker_compose,omitempty"`
	DockerRegistryImageName *string `json:"docker_registry_image_name,omitempty"`
	DockerRegistryImageTag  *string `json:"docker_registry_image_tag,omitempty"`
	DockerRegistryID        *int64  `json:"docker_registry_id,omitempty"`
	StaticImage             *string `json:"static_image,omitempty"`

	// Health checks — server applies sensible defaults if unset.
	HealthCheckEnabled *bool   `json:"health_check_enabled,omitempty"`
	HealthCheckPath    *string `json:"health_check_path,omitempty"`
}

// updateApp PATCHes an existing application. Mirrors the inline `patchApp`
// helper used by `myserver up` for image fields, but takes a typed
// request so `myserver app update` and the MCP tool can share validation
// + serialisation. Empty-string values are PRESERVED in the body — the
// server reads "" as "clear this field", which is how we expose
// --clear-image and similar.
func (a *apiClient) updateApp(id int64, req UpdateApplicationRequest) error {
	return a.do("PATCH", fmt.Sprintf("/api/v1/applications/%d", id), req, nil)
}

func (a *apiClient) createApp(req CreateApplicationRequest) (*Application, error) {
	var app Application
	if err := a.do("POST", "/api/v1/applications/", req, &app); err != nil {
		return nil, err
	}
	return &app, nil
}

func (a *apiClient) getApp(id int64) (*Application, error) {
	var app Application
	if err := a.do("GET", fmt.Sprintf("/api/v1/applications/%d", id), nil, &app); err != nil {
		return nil, err
	}
	return &app, nil
}

func (a *apiClient) patchApp(id int64, body map[string]any) error {
	return a.do("PATCH", fmt.Sprintf("/api/v1/applications/%d", id), body, nil)
}

// generateFQDN auto-assigns a public hostname for an app that doesn't have
// one. Used by `myserver up` so the first deploy of a freshly-created app
// is reachable without the customer having to set the FQDN by hand. The
// server picks a sensible default base domain (sslip.io for raw IPs,
// or a team-registered wildcard if one is configured).
func (a *apiClient) generateFQDN(id int64) (*Application, error) {
	var app Application
	// Empty body: server picks the base domain. We don't expose
	// base_domain / team_domain_id on the CLI surface today — keep it
	// simple, customer can override the FQDN later via PATCH if they
	// want a custom domain.
	if err := a.do("POST", fmt.Sprintf("/api/v1/applications/%d/generate-fqdn", id), map[string]any{}, &app); err != nil {
		return nil, err
	}
	return &app, nil
}

func (a *apiClient) deployApp(id int64) (*Deployment, error) {
	var d Deployment
	if err := a.do("POST", fmt.Sprintf("/api/v1/applications/%d/deploy", id), map[string]any{}, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (a *apiClient) listDeployments(appID int64, limit int) ([]Deployment, error) {
	if limit <= 0 {
		limit = 5
	}
	var d []Deployment
	if err := a.do("GET",
		fmt.Sprintf("/api/v1/applications/%d/deployments?limit=%d", appID, limit), nil, &d); err != nil {
		return nil, err
	}
	return d, nil
}

func (a *apiClient) getDeployment(appID, deployID int64) (*Deployment, error) {
	var d Deployment
	if err := a.do("GET",
		fmt.Sprintf("/api/v1/applications/%d/deployments/%d", appID, deployID), nil, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// sourceTarball POSTs a multipart tarball to /applications/{id}/source-tarball.
// The server extracts on the build target, creates a deployment row with
// SourceTarballPath set, and enqueues the deploy. Returns the new deployment.
//
// Used for `myserver up` against non-dockerimage build packs (railpack /
// dockerfile / dockercompose / static), where we want to replace the
// pipeline's `git clone` stage with the customer's local checkout.
func (a *apiClient) sourceTarball(appID int64, body io.Reader) (*Deployment, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		defer pw.Close()
		defer mw.Close()
		fw, err := mw.CreateFormFile("tarball", "source.tar.gz")
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(fw, body); err != nil {
			pw.CloseWithError(err)
		}
	}()
	req, err := http.NewRequest("POST",
		fmt.Sprintf("%s/api/v1/applications/%d/source-tarball", a.url, appID), pr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("X-Team-ID", fmt.Sprintf("%d", a.teamID))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	// Source upload + deploy enqueue is fast (no build), but the upload
	// itself can be slow on poor uplinks. 5 minutes is plenty.
	hc := &http.Client{Timeout: 5 * time.Minute}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		var errPayload struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(respBody, &errPayload) == nil && errPayload.Error != "" {
			return nil, fmt.Errorf("source-tarball: %d %s", resp.StatusCode, errPayload.Error)
		}
		return nil, fmt.Errorf("source-tarball: %d %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var envelope struct {
		Deployment Deployment `json:"deployment"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return nil, fmt.Errorf("decode source-tarball response: %w", err)
	}
	return &envelope.Deployment, nil
}

// buildTarball POSTs a multipart upload to /applications/{id}/build-tarball
// and streams the response back through onLine. Returns the parsed image
// repo + tag from the final `OK image_repo=... tag=...` line.
func (a *apiClient) buildTarball(appID int64, tag string, body io.Reader, onLine func(string)) (imageRepo, imageTag string, err error) {
	q := ""
	if tag != "" {
		q = "?tag=" + tag
	}
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		defer pw.Close()
		defer mw.Close()
		fw, err := mw.CreateFormFile("tarball", "source.tar.gz")
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(fw, body); err != nil {
			pw.CloseWithError(err)
		}
	}()
	req, err := http.NewRequest("POST",
		fmt.Sprintf("%s/api/v1/applications/%d/build-tarball%s", a.url, appID, q), pr)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("X-Team-ID", fmt.Sprintf("%d", a.teamID))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	// Build can be slow — disable the default 30s timeout for this call only.
	hc := &http.Client{Timeout: 0}
	resp, err := hc.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("build-tarball: %d %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		onLine(line)
		if strings.HasPrefix(line, "OK ") {
			imageRepo, imageTag = parseOKLine(line)
		}
		if strings.HasPrefix(line, "ERR ") {
			return "", "", fmt.Errorf("%s", strings.TrimPrefix(line, "ERR "))
		}
	}
	if err := scanner.Err(); err != nil {
		return "", "", fmt.Errorf("read stream: %w", err)
	}
	if imageRepo == "" || imageTag == "" {
		return "", "", fmt.Errorf("server didn't emit OK image_repo=... tag=... line")
	}
	return imageRepo, imageTag, nil
}

// parseOKLine reads the final "OK image_repo=... tag=... sha256=..." line.
func parseOKLine(line string) (imageRepo, imageTag string) {
	for _, kv := range strings.Fields(strings.TrimPrefix(line, "OK ")) {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		switch kv[:eq] {
		case "image_repo":
			imageRepo = kv[eq+1:]
		case "tag":
			imageTag = kv[eq+1:]
		}
	}
	return
}

// User — minimal shape returned by /auth/me.
type User struct {
	ID            int64  `json:"id"`
	Email         string `json:"email"`
	Name          string `json:"name"`
	IsSystemAdmin bool   `json:"is_system_admin"`
}

func (a *apiClient) getMe() (*User, error) {
	var u User
	if err := a.do("GET", "/api/v1/auth/me", nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// Project — workspace under a team. Holds environments.
type Project struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	TeamID      int64  `json:"team_id"`
}

func (a *apiClient) listProjects() ([]Project, error) {
	var p []Project
	if err := a.do("GET", "/api/v1/projects/", nil, &p); err != nil {
		return nil, err
	}
	return p, nil
}

// CreateProjectRequest mirrors the server's POST /projects body.
// Only `name` is required; restart policy fields are project-wide
// defaults inherited by environments and apps.
type CreateProjectRequest struct {
	Name              string  `json:"name"`
	Description       *string `json:"description,omitempty"`
	RestartPolicy     *string `json:"restart_policy,omitempty"`
	RestartMaxRetries *int    `json:"restart_max_retries,omitempty"`
}

func (a *apiClient) createProject(req CreateProjectRequest) (*Project, error) {
	var p Project
	if err := a.do("POST", "/api/v1/projects/", req, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// deleteProject removes a project. The server cascades to its
// environments and any resources within them — call sites must
// surface that to the user before invoking this.
func (a *apiClient) deleteProject(id int64) error {
	return a.do("DELETE", fmt.Sprintf("/api/v1/projects/%d", id), nil, nil)
}

// Environment — a deploy target inside a project (e.g. production, staging).
type Environment struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	ProjectID int64  `json:"project_id"`
	TeamID    int64  `json:"team_id"`
}

func (a *apiClient) listEnvironments(projectID int64) ([]Environment, error) {
	var e []Environment
	if err := a.do("GET", fmt.Sprintf("/api/v1/projects/%d/environments", projectID), nil, &e); err != nil {
		return nil, err
	}
	return e, nil
}

// CreateEnvironmentRequest mirrors the server's POST
// /projects/{projectId}/environments body. Restart policy fields are
// optional; if unset, the environment inherits the project's defaults.
type CreateEnvironmentRequest struct {
	Name              string  `json:"name"`
	Description       *string `json:"description,omitempty"`
	RestartPolicy     *string `json:"restart_policy,omitempty"`
	RestartMaxRetries *int    `json:"restart_max_retries,omitempty"`
}

func (a *apiClient) createEnvironment(projectID int64, req CreateEnvironmentRequest) (*Environment, error) {
	var e Environment
	if err := a.do("POST", fmt.Sprintf("/api/v1/projects/%d/environments", projectID), req, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// EnvironmentDeletionSummary mirrors envservice.DeletionSummary on
// the wire. We only need the counts + names to render a
// "this will delete N apps, M databases…" preview before the user
// confirms; we don't model the rest of the service shape.
type EnvironmentDeletionSummary struct {
	Applications      int      `json:"applications"`
	ApplicationNames  []string `json:"application_names"`
	Databases         int      `json:"databases"`
	DatabaseNames     []string `json:"database_names"`
	Services          int      `json:"services"`
	ServiceNames      []string `json:"service_names"`
	ResourceLinks     int      `json:"resource_links"`
	Workspaces        int      `json:"workspaces"`
	WorkspaceNames    []string `json:"workspace_names"`
	TotalResourceRefs int      `json:"total_resource_refs"`
}

func (a *apiClient) environmentDeletionSummary(id int64) (*EnvironmentDeletionSummary, error) {
	var s EnvironmentDeletionSummary
	if err := a.do("GET", fmt.Sprintf("/api/v1/environments/%d/deletion-summary", id), nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// deleteEnvironment removes an environment. The server returns 409
// Conflict if the env still contains resources and force is false —
// callers should fetch environmentDeletionSummary first, prompt the
// user, then re-call with force=true.
func (a *apiClient) deleteEnvironment(id int64, force bool) error {
	q := ""
	if force {
		q = "?force=true"
	}
	return a.do("DELETE", fmt.Sprintf("/api/v1/environments/%d%s", id, q), nil, nil)
}

// startApp / stopApp / restartApp — app lifecycle on a specific server.
// server_id is required because an app can be deployed to multiple servers.
// deleteApp soft-deletes an application. Cascade-removes deployments,
// env vars, sqlite resources, app tokens. Server containers are
// cleaned up by the worker via application:cleanup-deleted.
func (a *apiClient) deleteApp(appID int64) error {
	return a.do("DELETE", fmt.Sprintf("/api/v1/applications/%d", appID), nil, nil)
}

func (a *apiClient) startApp(appID, serverID int64) error {
	return a.do("POST",
		fmt.Sprintf("/api/v1/applications/%d/start", appID),
		map[string]any{"server_id": serverID}, nil)
}

func (a *apiClient) stopApp(appID, serverID int64) error {
	return a.do("POST",
		fmt.Sprintf("/api/v1/applications/%d/stop", appID),
		map[string]any{"server_id": serverID}, nil)
}

// restartApp issues POST /applications/{id}/restart on the given server.
// serverID is required because an app can be deployed to multiple servers.
func (a *apiClient) restartApp(appID, serverID int64) error {
	return a.do("POST",
		fmt.Sprintf("/api/v1/applications/%d/restart", appID),
		map[string]any{"server_id": serverID}, nil)
}

// appRuntimeLogs fetches recent runtime container logs (not deploy logs).
// lines is clamped server-side to [1, 5000]; 0 means use the server default (200).
func (a *apiClient) appRuntimeLogs(appID int64, lines int) (string, error) {
	path := fmt.Sprintf("/api/v1/applications/%d/logs", appID)
	if lines > 0 {
		path += fmt.Sprintf("?lines=%d", lines)
	}
	var resp struct {
		Logs string `json:"logs"`
	}
	if err := a.do("GET", path, nil, &resp); err != nil {
		return "", err
	}
	return resp.Logs, nil
}

// EnvVar — application-scoped environment variable.
// Value is omitted from list responses by default unless the caller asked for
// resolved values; tools should rely on key/metadata for read paths.
type EnvVar struct {
	ID          int64  `json:"id"`
	UUID        string `json:"uuid"`
	Key         string `json:"key"`
	Value       string `json:"value,omitempty"`
	IsLiteral   bool   `json:"is_literal"`
	IsRuntime   bool   `json:"is_runtime"`
	IsBuildtime bool   `json:"is_buildtime"`
	IsPreview   bool   `json:"is_preview"`
}

func (a *apiClient) listAppEnvVars(appID int64) ([]EnvVar, error) {
	var vars []EnvVar
	if err := a.do("GET", fmt.Sprintf("/api/v1/applications/%d/env-vars", appID), nil, &vars); err != nil {
		return nil, err
	}
	return vars, nil
}

type CreateEnvVarRequest struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	IsLiteral   bool   `json:"is_literal"`
	IsRuntime   bool   `json:"is_runtime"`
	IsBuildtime bool   `json:"is_buildtime"`
}

func (a *apiClient) createAppEnvVar(appID int64, req CreateEnvVarRequest) (*EnvVar, error) {
	var ev EnvVar
	if err := a.do("POST", fmt.Sprintf("/api/v1/applications/%d/env-vars", appID), req, &ev); err != nil {
		return nil, err
	}
	return &ev, nil
}

func (a *apiClient) deleteEnvVar(id int64) error {
	return a.do("DELETE", fmt.Sprintf("/api/v1/env-vars/%d", id), nil, nil)
}

// Database — managed standalone DB resource (postgres/mysql/redis/etc.).
// SQLite is intentionally NOT in this list — see SQLiteResource for that.
type Database struct {
	ID            int64  `json:"id"`
	UUID          string `json:"uuid"`
	Name          string `json:"name"`
	DatabaseType  string `json:"database_type"`
	Image         string `json:"image"`
	EnvironmentID int64  `json:"environment_id"`
	Status        string `json:"status"`
	IsPublic      bool   `json:"is_public"`
	PublicPort    *int   `json:"public_port,omitempty"`
}

func (a *apiClient) listDatabases() ([]Database, error) {
	var d []Database
	if err := a.do("GET", "/api/v1/databases/", nil, &d); err != nil {
		return nil, err
	}
	return d, nil
}

func (a *apiClient) getDatabase(id int64) (*Database, error) {
	var d Database
	if err := a.do("GET", fmt.Sprintf("/api/v1/databases/%d", id), nil, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (a *apiClient) restartDatabase(id int64) error {
	return a.do("POST", fmt.Sprintf("/api/v1/databases/%d/restart", id), map[string]any{}, nil)
}

func (a *apiClient) startDatabase(id int64) error {
	return a.do("POST", fmt.Sprintf("/api/v1/databases/%d/start", id), map[string]any{}, nil)
}

func (a *apiClient) stopDatabase(id int64) error {
	return a.do("POST", fmt.Sprintf("/api/v1/databases/%d/stop", id), map[string]any{}, nil)
}

// deleteDatabase soft-deletes the managed database. Volume retention
// is server-side: by default the underlying Docker volume is
// preserved so a recreate-with-same-name recovers data. The destroy-
// volume flag is server-side and not yet exposed here.
func (a *apiClient) deleteDatabase(id int64) error {
	return a.do("DELETE", fmt.Sprintf("/api/v1/databases/%d", id), nil, nil)
}

// SQLiteResource — managed SQLite (volume + env var injection) attached to one app.
type SQLiteResource struct {
	ID               int64  `json:"id"`
	UUID             string `json:"uuid"`
	Name             string `json:"name"`
	ApplicationID    int64  `json:"application_id"`
	FilePath         string `json:"file_path"`
	EnvVarKey        string `json:"env_var_key"`
	PragmaJournal    string `json:"pragma_journal"`
	Status           string `json:"status"`
	ConnectionString string `json:"connection_string"`
}

func (a *apiClient) listSqliteResources() ([]SQLiteResource, error) {
	var rs []SQLiteResource
	if err := a.do("GET", "/api/v1/sqlite", nil, &rs); err != nil {
		return nil, err
	}
	return rs, nil
}

type CreateSQLiteRequest struct {
	Name          string `json:"name"`
	EnvironmentID int64  `json:"environment_id"`
	ApplicationID int64  `json:"application_id"`
	FilePath      string `json:"file_path,omitempty"`
	EnvVarKey     string `json:"env_var_key,omitempty"`
	PragmaJournal string `json:"pragma_journal,omitempty"`
}

func (a *apiClient) createSqliteResource(req CreateSQLiteRequest) (*SQLiteResource, error) {
	var r SQLiteResource
	if err := a.do("POST", "/api/v1/sqlite", req, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (a *apiClient) getSqliteResource(id int64) (*SQLiteResource, error) {
	var r SQLiteResource
	if err := a.do("GET", fmt.Sprintf("/api/v1/sqlite/%d", id), nil, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// AppServiceToken mirrors the server-side row, minus the plaintext
// (which exists only in CreateAppTokenResponse).
type AppServiceToken struct {
	ID            int64    `json:"id"`
	UUID          string   `json:"uuid"`
	ApplicationID int64    `json:"application_id"`
	TeamID        int64    `json:"team_id"`
	TokenPrefix   string   `json:"token_prefix"`
	Scopes        []string `json:"scopes"`
	Name          string   `json:"name"`
	AutoInject    bool     `json:"auto_inject"`
	CreatedBy     int64    `json:"created_by"`
	ExpiresAt     *string  `json:"expires_at,omitempty"`
	LastUsedAt    *string  `json:"last_used_at,omitempty"`
	RevokedAt     *string  `json:"revoked_at,omitempty"`
	CreatedAt     string   `json:"created_at"`
}

// CreateAppTokenRequest is the wire shape POSTed to
// /api/v1/applications/{id}/tokens. expires_at is optional; nil =
// never expires.
type CreateAppTokenRequest struct {
	Name       string   `json:"name"`
	Scopes     []string `json:"scopes"`
	AutoInject bool     `json:"auto_inject,omitempty"`
	ExpiresAt  *string  `json:"expires_at,omitempty"`
}

// CreateAppTokenResponse carries the ONLY copy of the plaintext token
// the caller will ever see. The CLI prints it once and warns the user
// to save it.
type CreateAppTokenResponse struct {
	Token     AppServiceToken `json:"token"`
	Plaintext string          `json:"plaintext"`
	Warning   string          `json:"warning"`
}

func (a *apiClient) createAppToken(appID int64, req CreateAppTokenRequest) (*CreateAppTokenResponse, error) {
	var resp CreateAppTokenResponse
	if err := a.do("POST", fmt.Sprintf("/api/v1/applications/%d/tokens", appID), req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (a *apiClient) listAppTokens(appID int64) ([]AppServiceToken, error) {
	var out []AppServiceToken
	if err := a.do("GET", fmt.Sprintf("/api/v1/applications/%d/tokens", appID), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *apiClient) revokeAppToken(id int64) error {
	return a.do("DELETE", fmt.Sprintf("/api/v1/tokens/%d", id), nil, nil)
}

// AppDomainsResponse mirrors the server's domainsResponse shape:
// just {domains: [...]}.
type AppDomainsResponse struct {
	Domains []string `json:"domains"`
}

func (a *apiClient) listAppDomains(appID int64) ([]string, error) {
	var resp AppDomainsResponse
	if err := a.do("GET", fmt.Sprintf("/api/v1/applications/%d/domains", appID), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Domains, nil
}

func (a *apiClient) addAppDomain(appID int64, hostname string) ([]string, error) {
	var resp AppDomainsResponse
	body := map[string]string{"hostname": hostname}
	if err := a.do("POST", fmt.Sprintf("/api/v1/applications/%d/domains", appID), body, &resp); err != nil {
		return nil, err
	}
	return resp.Domains, nil
}

// removeAppDomain returns a 204 with no body, hence no response struct.
func (a *apiClient) removeAppDomain(appID int64, hostname string) error {
	// Hostnames may legitimately contain wildcards (`*`) and dots —
	// neither needs escaping in chi path-params, but we still pass it
	// through url-safely just in case a future path-param encoding
	// changes the rules.
	return a.do("DELETE", fmt.Sprintf("/api/v1/applications/%d/domains/%s", appID, hostname), nil, nil)
}

// deleteSqliteResource detaches a SQLite resource. If deleteVolume is true,
// the underlying volume row is removed too (data loss). Default false
// preserves the volume for accidental-undo.
func (a *apiClient) deleteSqliteResource(id int64, deleteVolume bool) error {
	q := ""
	if deleteVolume {
		q = "?delete_volume=true"
	}
	return a.do("DELETE", fmt.Sprintf("/api/v1/sqlite/%d%s", id, q), nil, nil)
}

func (a *apiClient) deploymentLogs(appID, deployID int64) ([]DeployLog, error) {
	var raw any
	if err := a.do("GET",
		fmt.Sprintf("/api/v1/applications/%d/deployments/%d/logs", appID, deployID), nil, &raw); err != nil {
		return nil, err
	}
	// The endpoint returns either a JSON array or {"logs": [...]} depending
	// on the version. Handle both shapes.
	switch v := raw.(type) {
	case []any:
		out := make([]DeployLog, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				out = append(out, mapToLog(m))
			}
		}
		return out, nil
	case map[string]any:
		if logs, ok := v["logs"].([]any); ok {
			out := make([]DeployLog, 0, len(logs))
			for _, item := range logs {
				if m, ok := item.(map[string]any); ok {
					out = append(out, mapToLog(m))
				}
			}
			return out, nil
		}
	}
	return nil, nil
}

func mapToLog(m map[string]any) DeployLog {
	str := func(k string) string {
		v, _ := m[k].(string)
		return v
	}
	return DeployLog{
		Src:     str("src"),
		Msg:     str("msg"),
		Command: str("command"),
		TS:      str("ts"),
	}
}
