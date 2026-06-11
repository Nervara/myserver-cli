package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func withStdin(t *testing.T, input string) {
	t.Helper()
	prev := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdin: %v", err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = prev
		_ = r.Close()
	})
}

func TestResolveTeamAPI_PersistsInteractiveTeamChoice(t *testing.T) {
	withFakeHome(t)
	requestedTeams := 0
	apiHitTeamID := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/teams":
			requestedTeams++
			fmt.Fprint(w, `[{"id":10,"name":"Alpha"},{"id":20,"name":"Beta"}]`)
		case "/api/v1/projects/":
			apiHitTeamID = r.Header.Get("X-Team-ID")
			fmt.Fprint(w, `[]`)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	if err := saveCredentials(&Credentials{APIURL: srv.URL, Token: "tok", Email: "user@example.com"}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}
	withStdin(t, "2\n")

	api, teamID, err := resolveTeamAPI(0, "")
	if err != nil {
		t.Fatalf("resolveTeamAPI: %v", err)
	}
	if teamID != 20 {
		t.Fatalf("teamID = %d, want 20", teamID)
	}
	if _, err := api.listProjects(); err != nil {
		t.Fatalf("listProjects: %v", err)
	}
	if apiHitTeamID != "20" {
		t.Fatalf("X-Team-ID = %q, want 20", apiHitTeamID)
	}
	if requestedTeams != 1 {
		t.Fatalf("teams requested %d times, want 1", requestedTeams)
	}
	creds, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials: %v", err)
	}
	if creds.CurrentTeamID != 20 {
		t.Fatalf("saved current team = %d, want 20", creds.CurrentTeamID)
	}
}

func TestResolveTeamAPI_UsesSavedTeamWithoutPrompting(t *testing.T) {
	withFakeHome(t)
	requestedTeams := 0
	apiHitTeamID := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/teams":
			requestedTeams++
			t.Fatalf("should not list teams when current team is saved")
		case "/api/v1/projects/":
			apiHitTeamID = r.Header.Get("X-Team-ID")
			fmt.Fprint(w, `[]`)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	if err := saveCredentials(&Credentials{APIURL: srv.URL, Token: "tok", Email: "user@example.com", CurrentTeamID: 20}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}

	api, teamID, err := resolveTeamAPI(0, "")
	if err != nil {
		t.Fatalf("resolveTeamAPI: %v", err)
	}
	if teamID != 20 {
		t.Fatalf("teamID = %d, want 20", teamID)
	}
	if _, err := api.listProjects(); err != nil {
		t.Fatalf("listProjects: %v", err)
	}
	if apiHitTeamID != "20" {
		t.Fatalf("X-Team-ID = %q, want 20", apiHitTeamID)
	}
	if requestedTeams != 0 {
		t.Fatalf("teams requested %d times, want 0", requestedTeams)
	}
}

func TestResolveTeamAPI_ExplicitTeamUpdatesSavedTeam(t *testing.T) {
	withFakeHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	if err := saveCredentials(&Credentials{APIURL: srv.URL, Token: "tok", Email: "user@example.com", CurrentTeamID: 20}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}

	_, teamID, err := resolveTeamAPI(30, "")
	if err != nil {
		t.Fatalf("resolveTeamAPI: %v", err)
	}
	if teamID != 30 {
		t.Fatalf("teamID = %d, want 30", teamID)
	}
	creds, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials: %v", err)
	}
	if creds.CurrentTeamID != 30 {
		t.Fatalf("saved current team = %d, want 30", creds.CurrentTeamID)
	}
}

func TestResolveTeamAPI_APIDoesNotOverwriteSavedCredentials(t *testing.T) {
	withFakeHome(t)
	override := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/teams":
			fmt.Fprint(w, `[{"id":99,"name":"Override"}]`)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer override.Close()

	if err := saveCredentials(&Credentials{APIURL: "https://saved.example", Token: "tok", Email: "user@example.com", CurrentTeamID: 20}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}

	_, teamID, err := resolveTeamAPI(99, override.URL)
	if err != nil {
		t.Fatalf("resolveTeamAPI: %v", err)
	}
	if teamID != 99 {
		t.Fatalf("teamID = %d, want 99", teamID)
	}
	creds, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials: %v", err)
	}
	if creds.APIURL != "https://saved.example" {
		t.Fatalf("saved APIURL = %q, want original", creds.APIURL)
	}
	if creds.CurrentTeamID != 20 {
		t.Fatalf("saved current team = %d, want original 20", creds.CurrentTeamID)
	}
}

func TestRunAppCreate_UsesSavedTeamWithoutPrompting(t *testing.T) {
	withFakeHome(t)
	apiHitTeamID := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/teams":
			t.Fatalf("should not list teams when current team is saved")
		case "/api/v1/applications/":
			apiHitTeamID = r.Header.Get("X-Team-ID")
			fmt.Fprint(w, `{"id":465,"name":"attachment-style-api","build_pack":"railpack"}`)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	if err := saveCredentials(&Credentials{APIURL: srv.URL, Token: "tok", Email: "user@example.com", CurrentTeamID: 20}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}

	if err := runAppCreate([]string{"--env=651", "--name=attachment-style-api", "--build-pack=railpack", "--ports-exposes=8080"}); err != nil {
		t.Fatalf("runAppCreate: %v", err)
	}
	if apiHitTeamID != "20" {
		t.Fatalf("X-Team-ID = %q, want 20", apiHitTeamID)
	}
}
