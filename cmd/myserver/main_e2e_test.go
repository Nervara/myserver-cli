package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// End-to-end smoke tests that build the CLI binary and invoke it as a real
// process. They guard the dispatcher (main.go), flag parsing, and the
// interactions between login → init → up → logs that pure unit tests miss.

var (
	binPathOnce sync.Once
	binPath     string
	binBuildErr error
)

// buildCLI compiles the CLI binary once per test process. Cached.
func buildCLI(t *testing.T) string {
	t.Helper()
	binPathOnce.Do(func() {
		dir, err := os.MkdirTemp("", "myserver-cli-bin-*")
		if err != nil {
			binBuildErr = err
			return
		}
		name := "myserver"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		binPath = filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-o", binPath, ".")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			binBuildErr = fmt.Errorf("build failed: %v: %s", err, stderr.String())
		}
	})
	if binBuildErr != nil {
		t.Fatalf("build CLI: %v", binBuildErr)
	}
	return binPath
}

func runCLI(t *testing.T, env map[string]string, stdin io.Reader, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(buildCLI(t), args...)
	cmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"USERPROFILE="+t.TempDir(),
	)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	if stdin != nil {
		cmd.Stdin = stdin
	}
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			t.Fatalf("run cli: %v", err)
		}
	}
	return so.String(), se.String(), exitCode
}

func TestE2E_Version(t *testing.T) {
	stdout, _, code := runCLI(t, nil, nil, "version")
	if code != 0 {
		t.Errorf("`version` exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "myserver-cli") {
		t.Errorf("version output = %q, expected to contain 'myserver-cli'", stdout)
	}
}

func TestE2E_NoArgsPrintsUsage(t *testing.T) {
	_, stderr, code := runCLI(t, nil, nil)
	if code != 1 {
		t.Errorf("no-args exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "Usage") {
		t.Errorf("no-args stderr should contain 'Usage', got: %q", stderr)
	}
}

func TestE2E_UnknownCommand(t *testing.T) {
	_, stderr, code := runCLI(t, nil, nil, "nope")
	if code != 2 {
		t.Errorf("unknown command exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr, "unknown command") {
		t.Errorf("expected 'unknown command' in stderr: %q", stderr)
	}
}

func TestE2E_HelpFlag(t *testing.T) {
	_, stderr, code := runCLI(t, nil, nil, "--help")
	if code != 0 {
		t.Errorf("--help exit code = %d, want 0", code)
	}
	for _, want := range []string{"auth", "login", "up", "init", "logs"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("--help missing command %q in:\n%s", want, stderr)
		}
	}
}

func TestE2E_AuthRegister(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %s", r.Method)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["name"] != "Alice" || body["email"] != "alice@example.com" || body["password"] != "password123" {
			t.Errorf("register body = %+v", body)
		}
		fmt.Fprint(w, `{"tokens":{"access_token":"AT","refresh_token":"RT","expires_at":1},"user":{"id":9,"email":"alice@example.com","name":"Alice"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, stderr, code := runCLI(t, nil, nil,
		"auth", "register",
		"--api", srv.URL,
		"--name", "Alice",
		"--email", "alice@example.com",
		"--password", "password123",
	)
	if code != 0 {
		t.Fatalf("auth register exit code = %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "registered alice@example.com") {
		t.Fatalf("stderr missing success message:\n%s", stderr)
	}
	if !strings.Contains(stderr, "warning: --password may be visible") {
		t.Fatalf("stderr missing password warning:\n%s", stderr)
	}
}

func TestE2E_AuthRegisterPasswordStdin(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/register", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["password"] != "secret with spaces" {
			t.Errorf("password = %q", body["password"])
		}
		fmt.Fprint(w, `{"tokens":{"access_token":"AT","refresh_token":"RT","expires_at":1},"user":{"id":9,"email":"alice@example.com","name":"Alice"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, stderr, code := runCLI(t, nil, strings.NewReader("secret with spaces\n"),
		"auth", "register",
		"--api", srv.URL,
		"--name", "Alice",
		"--email", "alice@example.com",
		"--password-stdin",
	)
	if code != 0 {
		t.Fatalf("auth register exit code = %d, stderr:\n%s", code, stderr)
	}
	if strings.Contains(stderr, "warning: --password") {
		t.Fatalf("stdin password should not print direct-password warning:\n%s", stderr)
	}
}

func TestE2E_AuthRegisterRejectsPasswordConflict(t *testing.T) {
	_, stderr, code := runCLI(t, nil, strings.NewReader("secret\n"),
		"auth", "register",
		"--name", "Alice",
		"--email", "alice@example.com",
		"--password", "secret",
		"--password-stdin",
	)
	if code == 0 {
		t.Fatalf("auth register should reject conflicting password inputs")
	}
	if !strings.Contains(stderr, "--password and --password-stdin are mutually exclusive") {
		t.Fatalf("stderr missing conflict message:\n%s", stderr)
	}
}

func TestE2E_RequiresLogin(t *testing.T) {
	// `init` and `up` should refuse to run without saved credentials.
	for _, sub := range []string{"init", "up"} {
		_, stderr, code := runCLI(t, nil, nil, sub)
		if code == 0 {
			t.Errorf("%s should fail without credentials", sub)
		}
		if !strings.Contains(strings.ToLower(stderr), "login") {
			t.Errorf("%s without creds should mention 'login'; got: %q", sub, stderr)
		}
	}
}

// TestE2E_UpEndToEnd boots an httptest server that simulates the full
// build/deploy API surface and runs `myserver up` against it. Verifies
// the binary correctly: tar-packs cwd, uploads multipart, parses the
// streamed OK line, PATCHes the app, triggers deploy, and tails logs.
func TestE2E_UpEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E in -short")
	}

	var (
		mu            sync.Mutex
		gotMultipart  bool
		gotPatch      bool
		gotDeployCall bool
		gotLogPolls   int
		// tailDeployment in cmd_up.go treats only {finished, failed, cancelled}
		// as terminal — using "finished" terminates the poll loop on the first
		// status call so the test doesn't sit on the 2s ticker.
		statusSequence = []string{"finished"}
		statusIdx      int
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tokens":{"access_token":"AT","refresh_token":"RT","expires_at":1},"user":{"id":1,"email":"a@b.com","name":"A"}}`)
	})
	mux.HandleFunc("/api/v1/applications/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/build-tarball") && r.Method == "POST":
			mu.Lock()
			gotMultipart = strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/")
			mu.Unlock()
			// Drain body so client-side write completes
			io.Copy(io.Discard, r.Body)
			fmt.Fprintln(w, "▸ extracting on host")
			fmt.Fprintln(w, "OK image_repo=registry/test tag=42 sha256=xx")

		case strings.HasSuffix(r.URL.Path, "/deploy") && r.Method == "POST":
			mu.Lock()
			gotDeployCall = true
			mu.Unlock()
			fmt.Fprint(w, `{"id":1,"application_id":99,"status":"queued","deployment_uuid":"u-1"}`)

		case strings.Contains(r.URL.Path, "/deployments/") && strings.HasSuffix(r.URL.Path, "/logs"):
			mu.Lock()
			gotLogPolls++
			mu.Unlock()
			fmt.Fprint(w, `[{"src":"deploy","msg":"step 1"}]`)

		case strings.Contains(r.URL.Path, "/deployments/"):
			mu.Lock()
			s := statusSequence[statusIdx]
			if statusIdx < len(statusSequence)-1 {
				statusIdx++
			}
			mu.Unlock()
			fmt.Fprintf(w, `{"id":1,"application_id":99,"status":%q}`, s)

		case r.Method == "PATCH":
			mu.Lock()
			gotPatch = true
			mu.Unlock()
			fmt.Fprint(w, `{}`)

		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/99"):
			fmt.Fprint(w, `{"id":99,"name":"myapp","build_pack":"dockerimage","fqdn":"myapp.example.com"}`)

		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Working dir with a Dockerfile so the tar pack has something to send.
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pre-seed credentials.json + myserver.json — bypasses login + init prompts.
	home := t.TempDir()
	credsDir := filepath.Join(home, ".myserver")
	if err := os.MkdirAll(credsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	creds := fmt.Sprintf(`{"api_url":%q,"token":"AT","email":"a@b.com"}`, srv.URL)
	if err := os.WriteFile(filepath.Join(credsDir, "credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	proj := `{"team_id":1,"app_id":99,"app_name":"myapp","build_pack":"dockerimage"}`
	if err := os.WriteFile(filepath.Join(work, "myserver.json"), []byte(proj), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := buildCLI(t)
	cmd := exec.Command(bin, "up")
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"USERPROFILE="+home,
	)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	if err := cmd.Run(); err != nil {
		t.Fatalf("`myserver up` failed: %v\nstdout=%s\nstderr=%s", err, so.String(), se.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if !gotMultipart {
		t.Error("server never received multipart upload")
	}
	if !gotPatch {
		t.Error("server never received PATCH /applications/99")
	}
	if !gotDeployCall {
		t.Error("server never received POST /applications/99/deploy")
	}
	if gotLogPolls == 0 {
		t.Error("server received no log polls")
	}
}

// TestE2E_UpFromSource exercises the new path: when the project's build_pack
// isn't "dockerimage", `myserver up` posts the source tarball to
// /source-tarball (which on the real server creates the deployment with
// SourceTarballPath set), then tails logs. No /build-tarball, no PATCH,
// no separate /deploy call.
func TestE2E_UpFromSource(t *testing.T) {
	var (
		mu                                        sync.Mutex
		gotSourceTarball, gotDeployCall, gotPatch bool
		gotBuildTarball                           bool
		gotLogPolls                               int
		statusSequence                            = []string{"queued", "in_progress", "in_progress", "finished"}
		statusIdx                                 int
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tokens":{"access_token":"AT","refresh_token":"RT","expires_at":1},"user":{"id":1,"email":"a@b.com","name":"A"}}`)
	})
	mux.HandleFunc("/api/v1/applications/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/source-tarball") && r.Method == "POST":
			mu.Lock()
			gotSourceTarball = strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/")
			mu.Unlock()
			io.Copy(io.Discard, r.Body)
			fmt.Fprint(w, `{"deployment":{"id":7,"application_id":99,"status":"queued","deployment_uuid":"u-7"},"source_tarball_path":"/tmp/myserver/cli-source/99-abc","sha256":"abc","size_bytes":42}`)

		case strings.HasSuffix(r.URL.Path, "/build-tarball") && r.Method == "POST":
			mu.Lock()
			gotBuildTarball = true
			mu.Unlock()
			fmt.Fprintln(w, "OK image_repo=x tag=y sha256=z")

		case strings.HasSuffix(r.URL.Path, "/deploy") && r.Method == "POST":
			mu.Lock()
			gotDeployCall = true
			mu.Unlock()
			fmt.Fprint(w, `{"id":1,"application_id":99,"status":"queued","deployment_uuid":"u-1"}`)

		case strings.Contains(r.URL.Path, "/deployments/") && strings.HasSuffix(r.URL.Path, "/logs"):
			mu.Lock()
			gotLogPolls++
			mu.Unlock()
			fmt.Fprint(w, `[{"src":"deploy","msg":"clone-skip: local source uploaded"}]`)

		case strings.Contains(r.URL.Path, "/deployments/"):
			mu.Lock()
			s := statusSequence[statusIdx]
			if statusIdx < len(statusSequence)-1 {
				statusIdx++
			}
			mu.Unlock()
			fmt.Fprintf(w, `{"id":7,"application_id":99,"status":%q}`, s)

		case r.Method == "PATCH":
			mu.Lock()
			gotPatch = true
			mu.Unlock()
			fmt.Fprint(w, `{}`)

		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/99"):
			fmt.Fprint(w, `{"id":99,"name":"tweetheart","build_pack":"railpack","fqdn":"tweetheart.example.com"}`)

		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "package.json"), []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	credsDir := filepath.Join(home, ".myserver")
	if err := os.MkdirAll(credsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	creds := fmt.Sprintf(`{"api_url":%q,"token":"AT","email":"a@b.com"}`, srv.URL)
	if err := os.WriteFile(filepath.Join(credsDir, "credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	proj := `{"team_id":1,"app_id":99,"app_name":"tweetheart","build_pack":"railpack"}`
	if err := os.WriteFile(filepath.Join(work, "myserver.json"), []byte(proj), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := buildCLI(t)
	cmd := exec.Command(bin, "up")
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "HOME="+home, "USERPROFILE="+home)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	if err := cmd.Run(); err != nil {
		t.Fatalf("`myserver up` failed: %v\nstdout=%s\nstderr=%s", err, so.String(), se.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if !gotSourceTarball {
		t.Error("server never received multipart /source-tarball upload")
	}
	if gotBuildTarball {
		t.Error("server received /build-tarball — should NOT happen for railpack build pack")
	}
	if gotPatch {
		t.Error("server received PATCH — source-tarball flow doesn't patch the app's image")
	}
	if gotDeployCall {
		t.Error("server received /deploy — source-tarball already creates the deployment")
	}
	if gotLogPolls == 0 {
		t.Error("server received no log polls")
	}
}

// TestE2E_UpAutoGeneratesFQDN verifies that when the bound app has no FQDN,
// `myserver up` calls /generate-fqdn before deploying. Closes the
// "first-deploy-on-a-new-app needs an extra UI click" gap.
func TestE2E_UpAutoGeneratesFQDN(t *testing.T) {
	var (
		mu              sync.Mutex
		gotGenerate     bool
		gotSourceUpload bool
		appFQDN         string // mutated by /generate-fqdn so the GET-after returns the new value
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tokens":{"access_token":"AT","refresh_token":"RT","expires_at":1},"user":{"id":1,"email":"a@b.com","name":"A"}}`)
	})
	mux.HandleFunc("/api/v1/applications/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/generate-fqdn") && r.Method == "POST":
			mu.Lock()
			gotGenerate = true
			appFQDN = "auto-99.sslip.io"
			fqdn := appFQDN
			mu.Unlock()
			fmt.Fprintf(w, `{"id":99,"name":"freshapp","build_pack":"railpack","fqdn":%q}`, fqdn)

		case strings.HasSuffix(r.URL.Path, "/source-tarball") && r.Method == "POST":
			mu.Lock()
			gotSourceUpload = true
			mu.Unlock()
			io.Copy(io.Discard, r.Body)
			fmt.Fprint(w, `{"deployment":{"id":7,"application_id":99,"status":"finished","deployment_uuid":"u-7"},"source_tarball_path":"/tmp/x","sha256":"abc","size_bytes":1}`)

		case strings.Contains(r.URL.Path, "/deployments/") && strings.HasSuffix(r.URL.Path, "/logs"):
			fmt.Fprint(w, `[]`)

		case strings.Contains(r.URL.Path, "/deployments/"):
			fmt.Fprint(w, `{"id":7,"application_id":99,"status":"finished"}`)

		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/99"):
			mu.Lock()
			fqdn := appFQDN
			mu.Unlock()
			fmt.Fprintf(w, `{"id":99,"name":"freshapp","build_pack":"railpack","fqdn":%q}`, fqdn)

		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "package.json"), []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	credsDir := filepath.Join(home, ".myserver")
	if err := os.MkdirAll(credsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	creds := fmt.Sprintf(`{"api_url":%q,"token":"AT","email":"a@b.com"}`, srv.URL)
	if err := os.WriteFile(filepath.Join(credsDir, "credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	// Project config has no fqdn — same as a fresh `myserver app create`.
	proj := `{"team_id":1,"app_id":99,"app_name":"freshapp","build_pack":"railpack"}`
	if err := os.WriteFile(filepath.Join(work, "myserver.json"), []byte(proj), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := buildCLI(t)
	cmd := exec.Command(bin, "up")
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "HOME="+home, "USERPROFILE="+home)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	if err := cmd.Run(); err != nil {
		t.Fatalf("`myserver up` failed: %v\nstdout=%s\nstderr=%s", err, so.String(), se.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if !gotGenerate {
		t.Error("server never received POST /generate-fqdn — auto-generate didn't fire")
	}
	if !gotSourceUpload {
		t.Error("server never received /source-tarball after generate")
	}
	if !strings.Contains(se.String(), "auto-99.sslip.io") {
		t.Errorf("expected stderr to print the generated FQDN, got: %s", se.String())
	}
}
