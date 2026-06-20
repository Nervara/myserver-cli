package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─── parseOKLine ──────────────────────────────────────────────────────

func TestParseOKLine(t *testing.T) {
	cases := []struct {
		line              string
		wantRepo, wantTag string
	}{
		{"OK image_repo=registry.local:5050/myapp tag=1714400000", "registry.local:5050/myapp", "1714400000"},
		{"OK tag=v1 image_repo=foo/bar sha256=deadbeef", "foo/bar", "v1"},
		{"OK", "", ""},
		{"OK only_one=value", "", ""},
		// real production-shaped line including image_id and digest
		{"OK image_repo=localhost:5050/myapp tag=42 image_id=sha256:abc digest=sha256:def", "localhost:5050/myapp", "42"},
	}
	for _, c := range cases {
		gotR, gotT := parseOKLine(c.line)
		if gotR != c.wantRepo || gotT != c.wantTag {
			t.Errorf("parseOKLine(%q) = (%q,%q), want (%q,%q)",
				c.line, gotR, gotT, c.wantRepo, c.wantTag)
		}
	}
}

// ─── apiClient.do  ───────────────────────────────────────────────────

func TestAPIClient_DoSetsHeaders(t *testing.T) {
	var gotAuth, gotTeam, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTeam = r.Header.Get("X-Team-ID")
		gotCT = r.Header.Get("Content-Type")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := newAPI(&Credentials{APIURL: srv.URL, Token: "tok-xyz"}, 42)
	if err := c.do("POST", "/api/v1/teams", map[string]any{"k": "v"}, nil); err != nil {
		t.Fatalf("do: %v", err)
	}
	if gotAuth != "Bearer tok-xyz" {
		t.Errorf("Authorization = %q, want Bearer tok-xyz", gotAuth)
	}
	if gotTeam != "42" {
		t.Errorf("X-Team-ID = %q, want 42", gotTeam)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}
}

func TestAPIClient_DoSurfacesErrorJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"nope"}`)
	}))
	defer srv.Close()

	c := newAPI(&Credentials{APIURL: srv.URL, Token: "t"}, 1)
	err := c.do("GET", "/x", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("expected error containing 'nope', got: %v", err)
	}
}

// ─── apiLogin ────────────────────────────────────────────────────────

func TestAPILogin_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/login" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["email"] != "a@b.com" || body["password"] != "secret" {
			t.Errorf("login payload = %+v", body)
		}
		fmt.Fprint(w, `{"tokens":{"access_token":"AT","refresh_token":"RT","expires_at":123},"user":{"id":7,"email":"a@b.com","name":"A"}}`)
	}))
	defer srv.Close()

	r, err := apiLogin(srv.URL, "a@b.com", "secret")
	if err != nil {
		t.Fatalf("apiLogin: %v", err)
	}
	if r.Tokens.AccessToken != "AT" {
		t.Errorf("access token = %q, want AT", r.Tokens.AccessToken)
	}
	if r.User.Email != "a@b.com" {
		t.Errorf("user email = %q", r.User.Email)
	}
}

func TestAPILogin_NoToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tokens":{}}`)
	}))
	defer srv.Close()

	_, err := apiLogin(srv.URL, "x", "y")
	if err == nil {
		t.Fatal("apiLogin: want error when access_token missing")
	}
}

func TestAPIRegister_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/auth/register" {
			t.Errorf("unexpected request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["name"] != "Alice" || body["email"] != "alice@example.com" || body["password"] != "password123" {
			t.Errorf("register payload = %+v", body)
		}
		fmt.Fprint(w, `{"tokens":{"access_token":"AT","refresh_token":"RT","expires_at":123},"user":{"id":9,"email":"alice@example.com","name":"Alice"}}`)
	}))
	defer srv.Close()

	r, err := apiRegister(srv.URL, RegisterUserRequest{
		Name:     "Alice",
		Email:    "alice@example.com",
		Password: "password123",
	})
	if err != nil {
		t.Fatalf("apiRegister: %v", err)
	}
	if r.Tokens.AccessToken != "AT" {
		t.Errorf("access token = %q, want AT", r.Tokens.AccessToken)
	}
	if r.User.ID != 9 || r.User.Email != "alice@example.com" {
		t.Errorf("user = %+v", r.User)
	}
}

func TestAPIGetMe_UnwrapsAuthMeResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/auth/me" {
			t.Errorf("unexpected request = %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"user":{"id":42,"email":"alice@example.com","name":"Alice","is_system_admin":true},"email_verified":true}`)
	}))
	defer srv.Close()

	api := newAPI(&Credentials{APIURL: srv.URL, Token: "AT"}, 0)
	user, err := api.getMe()
	if err != nil {
		t.Fatalf("getMe: %v", err)
	}
	if user.ID != 42 || user.Email != "alice@example.com" || user.Name != "Alice" || !user.IsSystemAdmin {
		t.Fatalf("user = %+v", user)
	}
}

// ─── listTeams / listApps / patch / deploy ───────────────────────────

func TestListTeamsAndApps(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/teams", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":1,"name":"alpha"},{"id":2,"name":"beta"}]`)
	})
	mux.HandleFunc("/api/v1/applications/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":10,"name":"myapp","build_pack":"dockerfile"}]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newAPI(&Credentials{APIURL: srv.URL, Token: "t"}, 1)

	teams, err := c.listTeams()
	if err != nil {
		t.Fatalf("listTeams: %v", err)
	}
	if len(teams) != 2 || teams[0].Name != "alpha" || teams[1].ID != 2 {
		t.Errorf("teams unexpected: %+v", teams)
	}

	apps, err := c.listApps()
	if err != nil {
		t.Fatalf("listApps: %v", err)
	}
	if len(apps) != 1 || apps[0].Name != "myapp" {
		t.Errorf("apps unexpected: %+v", apps)
	}
}

func TestDeployApp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/applications/99/deploy" {
			t.Errorf("unexpected req: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"id":555,"application_id":99,"status":"queued","deployment_uuid":"u-1"}`)
	}))
	defer srv.Close()

	c := newAPI(&Credentials{APIURL: srv.URL, Token: "t"}, 1)
	d, err := c.deployApp(99)
	if err != nil {
		t.Fatalf("deployApp: %v", err)
	}
	if d.ID != 555 || d.Status != "queued" {
		t.Errorf("deploy resp = %+v", d)
	}
}

func TestSourceSyncPlan_PostsManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/applications/42/source-sync/plan" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var req sourceManifest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode manifest: %v", err)
		}
		if req.Hash != "abc" || len(req.Files) != 1 || req.Files[0].Path != "main.go" {
			t.Fatalf("manifest request = %+v", req)
		}
		fmt.Fprint(w, `{"cache_ready":true,"manifest_hash":"abc","missing_paths":["main.go"],"delete_paths":["old.css"],"reused_files":2,"upload_files":1,"upload_bytes":12}`)
	}))
	defer srv.Close()

	c := newAPI(&Credentials{APIURL: srv.URL, Token: "t"}, 99)
	plan, err := c.sourceSyncPlan(42, &sourceManifest{
		Hash:  "abc",
		Files: []sourceManifestFile{{Path: "main.go", SHA256: "sha", Size: 12, Mode: 0o644}},
	})
	if err != nil {
		t.Fatalf("sourceSyncPlan: %v", err)
	}
	if plan.UploadBytes != 12 || len(plan.DeletePaths) != 1 || plan.DeletePaths[0] != "old.css" {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestSourceSync_UploadsDeltaTarball(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/applications/42/source-sync" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("manifest_hash") != "abc123" {
			t.Fatalf("manifest_hash = %q", r.URL.Query().Get("manifest_hash"))
		}
		ct := r.Header.Get("Content-Type")
		mediaType, params, err := mime.ParseMediaType(ct)
		if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
			t.Fatalf("unexpected content-type: %s", ct)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		part, err := mr.NextPart()
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		if part.FormName() != "tarball" {
			t.Fatalf("form name = %q", part.FormName())
		}
		body, _ := io.ReadAll(part)
		if !bytes.HasPrefix(body, []byte{0x1f, 0x8b}) {
			t.Fatalf("upload is not gzip")
		}
		fmt.Fprint(w, `{"deployment":{"id":7,"application_id":42,"status":"queued","deployment_uuid":"u-7"},"source_tarball_path":"/tmp/x","manifest_hash":"abc123"}`)
	}))
	defer srv.Close()

	var tarball bytes.Buffer
	gz := gzip.NewWriter(&tarball)
	_, _ = gz.Write([]byte("delta"))
	_ = gz.Close()

	c := newAPI(&Credentials{APIURL: srv.URL, Token: "t"}, 1)
	dep, err := c.sourceSync(42, "abc123", &tarball)
	if err != nil {
		t.Fatalf("sourceSync: %v", err)
	}
	if dep.ID != 7 || dep.Status != "queued" {
		t.Fatalf("deployment = %+v", dep)
	}
}

// ─── buildTarball ────────────────────────────────────────────────────

func TestBuildTarball_StreamsOKLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify multipart upload arrived intact.
		ct := r.Header.Get("Content-Type")
		mediaType, params, err := mime.ParseMediaType(ct)
		if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
			t.Errorf("unexpected content-type: %s", ct)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		part, err := mr.NextPart()
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		if part.FormName() != "tarball" {
			t.Errorf("form name = %q", part.FormName())
		}
		body, _ := io.ReadAll(part)
		if !bytes.HasPrefix(body, []byte{0x1f, 0x8b}) {
			t.Errorf("upload body is not gzip; first bytes = %x", body[:min(4, len(body))])
		}

		// Stream a few progress lines, then the terminating OK line.
		flush, _ := w.(http.Flusher)
		fmt.Fprintln(w, "▸ extracting on host")
		flush.Flush()
		fmt.Fprintln(w, "▸ docker build ...")
		flush.Flush()
		fmt.Fprintln(w, "OK image_repo=localhost:5050/myapp tag=1714 sha256=abc")
	}))
	defer srv.Close()

	// Build a small gzipped buffer to act as the tarball.
	var src bytes.Buffer
	gz := gzip.NewWriter(&src)
	gz.Write([]byte("not-actually-a-tar-but-good-enough"))
	gz.Close()

	c := newAPI(&Credentials{APIURL: srv.URL, Token: "t"}, 1)

	var seen []string
	repo, tag, err := c.buildTarball(99, "1714", &src, func(line string) {
		seen = append(seen, line)
	})
	if err != nil {
		t.Fatalf("buildTarball: %v", err)
	}
	if repo != "localhost:5050/myapp" || tag != "1714" {
		t.Errorf("parsed repo/tag = %q/%q", repo, tag)
	}
	if len(seen) != 3 {
		t.Errorf("expected 3 streamed lines, got %d: %v", len(seen), seen)
	}
}

func TestBuildTarball_PropagatesERRLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ERR docker build failed: nonzero exit 1")
	}))
	defer srv.Close()

	c := newAPI(&Credentials{APIURL: srv.URL, Token: "t"}, 1)
	_, _, err := c.buildTarball(1, "", strings.NewReader(""), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "docker build failed") {
		t.Errorf("expected build error, got: %v", err)
	}
}

func TestBuildTarball_MissingOKLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "▸ doing things")
		fmt.Fprintln(w, "▸ doing more things")
	}))
	defer srv.Close()

	c := newAPI(&Credentials{APIURL: srv.URL, Token: "t"}, 1)
	_, _, err := c.buildTarball(1, "", strings.NewReader(""), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "OK") {
		t.Errorf("expected missing-OK error, got: %v", err)
	}
}

func TestBuildTarball_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, "unauthorized")
	}))
	defer srv.Close()

	c := newAPI(&Credentials{APIURL: srv.URL, Token: "bad"}, 1)
	_, _, err := c.buildTarball(1, "", strings.NewReader(""), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 error, got: %v", err)
	}
}
