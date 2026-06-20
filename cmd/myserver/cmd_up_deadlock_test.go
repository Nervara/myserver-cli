package main

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunUpFromSource_ServerEarlyResponseDoesNotDeadlock(t *testing.T) {
	dir := t.TempDir()
	chdirTo(t, dir)
	if err := os.WriteFile("large.bin", make([]byte, 8<<20), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/applications/42/source-sync/plan" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/api/v1/applications/42/source-tarball" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		// Intentionally do not read r.Body. This reproduces an early server
		// response while the CLI tar writer may still be blocked on io.Pipe.
		fmt.Fprint(w, `{"id":99,"status":"queued"}`)
	}))
	defer srv.Close()

	api := newAPI(&Credentials{APIURL: srv.URL, Token: "tok"}, 7)
	done := make(chan error, 1)
	go func() {
		done <- runUpFromSource(api, &ProjectConfig{AppID: 42}, false, true)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runUpFromSource: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runUpFromSource deadlocked after early server response")
	}
}

func TestRunUpFromSource_UsesSourceSyncCache(t *testing.T) {
	dir := t.TempDir()
	chdirTo(t, dir)
	mustWrite(t, filepath.Join(dir, "main.go"), "package main\n")
	mustWrite(t, filepath.Join(dir, "public", "logo.png"), "png")

	var sawPlan, sawSync, sawTarball bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/applications/42/source-sync/plan":
			sawPlan = true
			fmt.Fprint(w, `{"cache_ready":true,"manifest_hash":"abc","missing_paths":["main.go"],"delete_paths":["old.css"],"reused_files":1,"upload_files":1,"upload_bytes":13}`)
		case "/api/v1/applications/42/source-sync":
			sawSync = true
			if r.URL.Query().Get("manifest_hash") == "" {
				t.Fatal("missing manifest_hash")
			}
			ct := r.Header.Get("Content-Type")
			mediaType, params, err := mime.ParseMediaType(ct)
			if err != nil || mediaType != "multipart/form-data" {
				t.Fatalf("unexpected content-type: %s", ct)
			}
			mr := multipart.NewReader(r.Body, params["boundary"])
			part, err := mr.NextPart()
			if err != nil {
				t.Fatalf("read multipart: %v", err)
			}
			body, err := io.ReadAll(part)
			if err != nil {
				t.Fatalf("read upload: %v", err)
			}
			names := gzipTarNames(t, bytes.NewReader(body))
			want := map[string]bool{
				".myserver-sync-delete":        true,
				".myserver-sync-manifest.json": true,
				"main.go":                      true,
			}
			if len(names) != len(want) {
				t.Fatalf("delta tar names = %v", names)
			}
			for _, name := range names {
				if !want[name] {
					t.Fatalf("unexpected delta tar entry %q in %v", name, names)
				}
			}
			fmt.Fprint(w, `{"deployment":{"id":99,"status":"queued"}}`)
		case "/api/v1/applications/42/source-tarball":
			sawTarball = true
			http.Error(w, "source-tarball should not be used", http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	api := newAPI(&Credentials{APIURL: srv.URL, Token: "tok"}, 7)
	if err := runUpFromSource(api, &ProjectConfig{AppID: 42}, false, true); err != nil {
		t.Fatalf("runUpFromSource: %v", err)
	}
	if !sawPlan || !sawSync || sawTarball {
		t.Fatalf("cache path calls: plan=%v sync=%v tarball=%v", sawPlan, sawSync, sawTarball)
	}
}
