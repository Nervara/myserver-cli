package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// chdirTo switches into dir for the duration of the test, restoring cwd
// after. Required because saveProjectConfig / loadProjectConfig hit
// `myserver.json` in the working directory.
func chdirTo(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

// withFakeHome rewrites $HOME to a temp dir for the duration of the test —
// loadCredentials/saveCredentials walk under $HOME/.myserver/.
func withFakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // belt-and-braces for Windows
	return home
}

func TestCredentials_RoundTrip(t *testing.T) {
	home := withFakeHome(t)

	in := &Credentials{
		APIURL:        "https://example.test",
		Token:         "tok-abc",
		Email:         "a@b.com",
		CurrentTeamID: 42,
	}
	if err := saveCredentials(in); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}

	// File mode must be 0600 on POSIX — the token is a long-lived secret.
	// Windows NTFS doesn't honor POSIX mode bits (os.Chmod is essentially
	// a no-op there), so os.Stat reports 0666 regardless of what we
	// asked for. Skip the assertion on Windows; the file's actual
	// access is controlled by ACLs, which we don't manage from Go.
	st, err := os.Stat(filepath.Join(home, credentialsPath))
	if err != nil {
		t.Fatalf("stat credentials: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := st.Mode().Perm(); perm != 0o600 {
			t.Errorf("credentials perms = %o, want 0600", perm)
		}
	}

	out, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials: %v", err)
	}
	if *out != *in {
		t.Errorf("round-trip mismatch: got=%+v want=%+v", out, in)
	}
}

func TestCredentials_NotLoggedIn(t *testing.T) {
	withFakeHome(t)

	_, err := loadCredentials()
	if !errors.Is(err, errNotLoggedIn) {
		t.Errorf("missing creds = %v, want errNotLoggedIn", err)
	}
}

func TestCredentials_RejectsEmptyToken(t *testing.T) {
	home := withFakeHome(t)
	path := filepath.Join(home, credentialsPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"api_url":"x","token":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadCredentials()
	if !errors.Is(err, errNotLoggedIn) {
		t.Errorf("empty token = %v, want errNotLoggedIn", err)
	}
}

func TestProjectConfig_RoundTrip(t *testing.T) {
	chdirTo(t, t.TempDir())

	in := &ProjectConfig{
		TeamID:       7,
		ServerID:     12,
		AppID:        99,
		AppName:      "myapp",
		BuildPack:    "dockerfile",
		PortsExposes: "3000",
		FQDN:         "myapp.example.com",
	}
	if err := saveProjectConfig(in); err != nil {
		t.Fatalf("saveProjectConfig: %v", err)
	}

	out, err := loadProjectConfig()
	if err != nil {
		t.Fatalf("loadProjectConfig: %v", err)
	}
	if *out != *in {
		t.Errorf("round-trip mismatch: got=%+v want=%+v", out, in)
	}
}

func TestProjectConfig_NoFile(t *testing.T) {
	chdirTo(t, t.TempDir())

	_, err := loadProjectConfig()
	if !errors.Is(err, errNoProject) {
		t.Errorf("no myserver.json = %v, want errNoProject", err)
	}
}

func TestProjectConfig_RejectsBadJSON(t *testing.T) {
	dir := t.TempDir()
	chdirTo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, projectConfigFn), []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadProjectConfig()
	if err == nil {
		t.Fatal("loadProjectConfig: want error on malformed JSON, got nil")
	}
}
