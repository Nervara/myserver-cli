package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestRunEnvCreate_CreatesMultipleRepeatedNames(t *testing.T) {
	names := runEnvCreateForNames(t,
		"--project=650",
		"--name=production",
		"--name=staging",
	)
	if !reflect.DeepEqual(names, []string{"production", "staging"}) {
		t.Fatalf("created names = %#v", names)
	}
}

func TestRunEnvCreate_CreatesMultiplePositionalNames(t *testing.T) {
	names := runEnvCreateForNames(t,
		"--project=650",
		"production",
		"staging",
	)
	if !reflect.DeepEqual(names, []string{"production", "staging"}) {
		t.Fatalf("created names = %#v", names)
	}
}

func TestRunEnvCreate_CreatesMultipleCommaSeparatedNames(t *testing.T) {
	names := runEnvCreateForNames(t,
		"--project=650",
		"--name=production,staging",
	)
	if !reflect.DeepEqual(names, []string{"production", "staging"}) {
		t.Fatalf("created names = %#v", names)
	}
}

func runEnvCreateForNames(t *testing.T, args ...string) []string {
	t.Helper()
	withFakeHome(t)
	var created []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/projects/650/environments":
			if got := r.Header.Get("X-Team-ID"); got != "20" {
				t.Fatalf("X-Team-ID = %q, want 20", got)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			created = append(created, body["name"])
			fmt.Fprintf(w, `{"id":%d,"name":%q}`, 650+len(created), body["name"])
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	if err := saveCredentials(&Credentials{APIURL: srv.URL, Token: "tok", Email: "user@example.com", CurrentTeamID: 20}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}
	if err := runEnvCreate(args); err != nil {
		t.Fatalf("runEnvCreate: %v", err)
	}
	return created
}
