// Tests for the apiClient methods that back `myserver sqlite *`. We
// stay at the client layer (rather than driving runSqliteCreate end to
// end) because that's where the wire contract with the server lives —
// the surrounding flag parsing is covered by `go vet` and the
// hand-runnable usage examples.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSqliteList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/sqlite" {
			t.Errorf("unexpected req: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `[{"id":1,"name":"primary","application_id":7,"file_path":"/data/primary.db","env_var_key":"DATABASE_URL","status":"active"}]`)
	}))
	defer srv.Close()

	c := newAPI(&Credentials{APIURL: srv.URL, Token: "t"}, 1)
	rs, err := c.listSqliteResources()
	if err != nil {
		t.Fatalf("listSqliteResources: %v", err)
	}
	if len(rs) != 1 || rs[0].Name != "primary" || rs[0].ApplicationID != 7 {
		t.Errorf("unexpected: %+v", rs)
	}
}

func TestSqliteCreate_PostsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/sqlite" {
			t.Errorf("unexpected req: %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("body not json: %v — %s", err, body)
		}
		// Spot-check that the fields the server cares about make it into the wire.
		if got["name"] != "primary" || got["application_id"].(float64) != 7 || got["environment_id"].(float64) != 3 {
			t.Errorf("required fields missing: %s", body)
		}
		if got["file_path"] != "/data/primary.db" {
			t.Errorf("file_path lost in transit: %s", body)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":11,"name":"primary","application_id":7,"file_path":"/data/primary.db","env_var_key":"DATABASE_URL","connection_string":"sqlite:///data/primary.db","status":"active"}`)
	}))
	defer srv.Close()

	c := newAPI(&Credentials{APIURL: srv.URL, Token: "t"}, 1)
	r, err := c.createSqliteResource(CreateSQLiteRequest{
		Name:          "primary",
		EnvironmentID: 3,
		ApplicationID: 7,
		FilePath:      "/data/primary.db",
	})
	if err != nil {
		t.Fatalf("createSqliteResource: %v", err)
	}
	if r.ID != 11 || r.ConnectionString != "sqlite:///data/primary.db" {
		t.Errorf("unexpected resp: %+v", r)
	}
}

func TestSqliteGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/sqlite/11" {
			t.Errorf("unexpected req: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"id":11,"name":"primary","application_id":7,"file_path":"/data/primary.db","env_var_key":"DATABASE_URL","status":"active"}`)
	}))
	defer srv.Close()

	c := newAPI(&Credentials{APIURL: srv.URL, Token: "t"}, 1)
	r, err := c.getSqliteResource(11)
	if err != nil {
		t.Fatalf("getSqliteResource: %v", err)
	}
	if r.ID != 11 || r.Name != "primary" {
		t.Errorf("unexpected resp: %+v", r)
	}
}

// The delete-volume query param is the one bit of behaviour easy to
// get wrong, so it gets its own table-driven check (mirrors
// TestMCP_DeleteSQLitePassesQueryParam in mcp_test.go).
func TestSqliteDelete_QueryParam(t *testing.T) {
	cases := []struct {
		name         string
		deleteVolume bool
		wantSuffix   string
	}{
		{"preserve volume by default", false, "/api/v1/sqlite/11"},
		{"opt-in to wipe volume", true, "/api/v1/sqlite/11?delete_volume=true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotURL string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "DELETE" {
					t.Errorf("want DELETE, got %s", r.Method)
				}
				gotURL = r.URL.String()
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()

			c := newAPI(&Credentials{APIURL: srv.URL, Token: "t"}, 1)
			if err := c.deleteSqliteResource(11, tc.deleteVolume); err != nil {
				t.Fatalf("deleteSqliteResource: %v", err)
			}
			if !strings.HasSuffix(gotURL, tc.wantSuffix) {
				t.Errorf("want URL suffix %q, got %q", tc.wantSuffix, gotURL)
			}
		})
	}
}

// runSqlite is the top-level router. We hit each branch with a noop
// transport so a bad subcommand string fails the build of the help
// text but doesn't accidentally call the API.
func TestRunSqlite_UnknownSubcommand(t *testing.T) {
	err := runSqlite([]string{"frobnicate"})
	if err == nil || !strings.Contains(err.Error(), "unknown sqlite subcommand") {
		t.Errorf("want unknown-subcommand error, got %v", err)
	}
}

func TestRunSqlite_NoArgs(t *testing.T) {
	err := runSqlite(nil)
	if err == nil || !strings.Contains(err.Error(), "no subcommand") {
		t.Errorf("want no-subcommand error, got %v", err)
	}
}
