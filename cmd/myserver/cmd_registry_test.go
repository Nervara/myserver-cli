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

func TestDockerRegistryCreate_PostsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/docker-registries/" {
			t.Errorf("unexpected req: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-Team-ID"); got != "10" {
			t.Errorf("X-Team-ID = %q, want 10", got)
		}
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("body not json: %v — %s", err, body)
		}
		if got["name"] != "acr-prod" || got["registry_url"] != "example.azurecr.io" ||
			got["username"] != "acr-user" || got["password"] != "secret" {
			t.Errorf("registry body missing fields: %s", body)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":44,"team_id":10,"name":"acr-prod","registry_url":"example.azurecr.io","username":"acr-user"}`)
	}))
	defer srv.Close()

	c := newAPI(&Credentials{APIURL: srv.URL, Token: "t"}, 10)
	reg, err := c.createDockerRegistry(CreateDockerRegistryRequest{
		Name:        "acr-prod",
		RegistryURL: "example.azurecr.io",
		Username:    "acr-user",
		Password:    "secret",
	})
	if err != nil {
		t.Fatalf("createDockerRegistry: %v", err)
	}
	if reg.ID != 44 || reg.RegistryURL != "example.azurecr.io" {
		t.Errorf("unexpected registry response: %+v", reg)
	}
}

func TestDockerRegistryList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/docker-registries/" {
			t.Errorf("unexpected req: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `[{"id":44,"team_id":10,"name":"acr-prod","registry_url":"example.azurecr.io","username":"acr-user","is_system":false}]`)
	}))
	defer srv.Close()

	c := newAPI(&Credentials{APIURL: srv.URL, Token: "t"}, 10)
	regs, err := c.listDockerRegistries()
	if err != nil {
		t.Fatalf("listDockerRegistries: %v", err)
	}
	if len(regs) != 1 || regs[0].Name != "acr-prod" {
		t.Errorf("unexpected registry list: %+v", regs)
	}
}

func TestRunRegistry_UnknownSubcommand(t *testing.T) {
	err := runRegistry([]string{"frobnicate"})
	if err == nil || !strings.Contains(err.Error(), "unknown registry subcommand") {
		t.Errorf("want unknown-subcommand error, got %v", err)
	}
}

func TestAppCreateRequestIncludesDockerRegistryID(t *testing.T) {
	id := int64(44)
	body, err := json.Marshal(CreateApplicationRequest{
		Name:             "private-app",
		EnvironmentID:    3,
		BuildPack:        "dockerimage",
		DockerRegistryID: &id,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"docker_registry_id":44`) {
		t.Fatalf("docker_registry_id missing from create app body: %s", body)
	}
}

func TestRunAppListHelp(t *testing.T) {
	if err := runApp([]string{"list", "-h"}); err != nil {
		t.Fatalf("app list help: %v", err)
	}
}

func TestRunAppGetHelp(t *testing.T) {
	if err := runApp([]string{"get", "-h"}); err != nil {
		t.Fatalf("app get help: %v", err)
	}
}

func TestRunAppDeployHelp(t *testing.T) {
	if err := runApp([]string{"deploy", "-h"}); err != nil {
		t.Fatalf("app deploy help: %v", err)
	}
}

func TestRunDeploymentGetHelp(t *testing.T) {
	if err := runDeployment([]string{"get", "-h"}); err != nil {
		t.Fatalf("deployment get help: %v", err)
	}
}

func TestRequiresReuseLastImage(t *testing.T) {
	gitURL := "https://github.com/acme/app.git"
	image := "registry.example.com/app"
	if !requiresReuseLastImageFlag(&Application{}) {
		t.Fatal("blank app should require --reuse-last-image")
	}
	if requiresReuseLastImageFlag(&Application{GitRepository: gitURL}) {
		t.Fatal("git app should not require --reuse-last-image")
	}
	if requiresReuseLastImageFlag(&Application{DockerRegistryImageName: image}) {
		t.Fatal("docker image app should not require --reuse-last-image")
	}
}
