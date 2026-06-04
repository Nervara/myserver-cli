package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// rpcLine encodes a JSON-RPC request as a single line (the wire format MCP
// uses over stdio).
func rpcLine(t *testing.T, id int, method string, params any) string {
	t.Helper()
	m := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		m["params"] = params
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("encode rpc: %v", err)
	}
	return string(b) + "\n"
}

// runServer drives the MCP server with a multi-line input and returns each
// JSON object the server wrote, parsed.
func runServer(t *testing.T, api *apiClient, input string) []map[string]any {
	t.Helper()
	var out, log bytes.Buffer
	srv := &mcpServer{api: api, out: &out, log: &log}
	if err := srv.serve(strings.NewReader(input)); err != nil {
		t.Fatalf("serve: %v", err)
	}

	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode response %q: %v", line, err)
		}
		responses = append(responses, m)
	}
	return responses
}

// fakeAPI builds an apiClient pointed at an httptest server with a custom handler.
func fakeAPI(handler http.HandlerFunc) (*apiClient, *httptest.Server) {
	srv := httptest.NewServer(handler)
	c := newAPI(&Credentials{APIURL: srv.URL, Token: "test-token"}, 7)
	return c, srv
}

func TestMCP_Initialize(t *testing.T) {
	resps := runServer(t, nil, rpcLine(t, 1, "initialize", map[string]any{}))
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	r := resps[0]
	if r["id"].(float64) != 1 {
		t.Errorf("id mismatch: %v", r["id"])
	}
	res, ok := r["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", r)
	}
	if res["protocolVersion"] != mcpProtocolVersion {
		t.Errorf("protocolVersion: %v", res["protocolVersion"])
	}
	caps, ok := res["capabilities"].(map[string]any)
	if !ok || caps["tools"] == nil {
		t.Errorf("expected tools capability, got %v", res["capabilities"])
	}
}

func TestMCP_ToolsList(t *testing.T) {
	resps := runServer(t, nil, rpcLine(t, 2, "tools/list", nil))
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	res := resps[0]["result"].(map[string]any)
	tools := res["tools"].([]any)
	if len(tools) < 8 {
		t.Errorf("expected ≥8 tools, got %d", len(tools))
	}

	// Spot-check the SQLite tool ships with a destructive annotation so
	// clients prompt for confirmation before invoking.
	var sqliteTool map[string]any
	for _, raw := range tools {
		t := raw.(map[string]any)
		if t["name"] == "create_sqlite_resource" {
			sqliteTool = t
			break
		}
	}
	if sqliteTool == nil {
		t.Fatal("create_sqlite_resource tool missing")
	}
	annot, _ := sqliteTool["annotations"].(map[string]any)
	if annot == nil || annot["destructiveHint"] != true {
		t.Errorf("create_sqlite_resource should have destructiveHint, got %v", annot)
	}
}

// TestMCP_DeploymentGuidanceInDescriptions enforces that the warnings
// AI clients depend on don't silently get edited away — these strings
// are how an agent learns the deploy-verification workflow and known
// bugs without having to read the codebase.
func TestMCP_DeploymentGuidanceInDescriptions(t *testing.T) {
	descByName := map[string]string{}
	for _, tool := range mcpTools {
		descByName[tool.Name] = tool.Description
	}

	wants := []struct {
		tool     string
		mustHave string
		why      string
	}{
		{"deploy_app", "cutover-orphan", "deploy_app must warn about the cutover-orphan bug on dockerimage redeploys"},
		{"deploy_app", "list_deployments", "deploy_app must steer agents to the verification workflow"},
		{"tail_app_logs", "no container found", "tail_app_logs must explain the 404 signal so agents diagnose correctly"},
		{"tail_deployment_logs", "tail_app_logs", "tail_deployment_logs must distinguish itself from tail_app_logs"},
		{"list_deployments", "queued", "list_deployments must enumerate the status values agents will see"},
		{"generate_fqdn", "BEFORE", "generate_fqdn must call out the timing requirement"},
		{"create_sqlite_resource", "set_env_var", "create_sqlite_resource must mention the env var workaround"},
	}
	for _, w := range wants {
		desc, ok := descByName[w.tool]
		if !ok {
			t.Errorf("tool %q not found in mcpTools", w.tool)
			continue
		}
		if !strings.Contains(desc, w.mustHave) {
			t.Errorf("%s — %q description missing %q. why: %s",
				w.tool, w.tool, w.mustHave, w.why)
		}
	}
}

func TestMCP_NotificationHasNoResponse(t *testing.T) {
	// Notifications (no id field) must produce no output.
	line := `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
	resps := runServer(t, nil, line)
	if len(resps) != 0 {
		t.Errorf("expected no response to notification, got %v", resps)
	}
}

func TestMCP_UnknownMethod(t *testing.T) {
	resps := runServer(t, nil, rpcLine(t, 9, "totally/madeup", nil))
	if len(resps) != 1 {
		t.Fatalf("expected 1 response")
	}
	errObj, ok := resps[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error envelope, got %v", resps[0])
	}
	if errObj["code"].(float64) != rpcMethodNotFound {
		t.Errorf("expected method-not-found code, got %v", errObj["code"])
	}
}

func TestMCP_ToolCallListApps(t *testing.T) {
	api, srv := fakeAPI(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/applications/" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":42,"name":"my-web-app","build_pack":"dockerfile","status":"running","fqdn":"web.example.com"}]`))
	})
	defer srv.Close()

	resps := runServer(t, api, rpcLine(t, 3, "tools/call", map[string]any{
		"name":      "list_apps",
		"arguments": map[string]any{},
	}))
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resps))
	}
	res := resps[0]["result"].(map[string]any)
	if res["isError"] == true {
		t.Errorf("unexpected error: %v", res)
	}
	content := res["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "#42 my-web-app") {
		t.Errorf("tool output missing app: %s", text)
	}
}

func TestMCP_ToolCallReportsErrorInResultEnvelope(t *testing.T) {
	api, srv := fakeAPI(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	})
	defer srv.Close()

	resps := runServer(t, api, rpcLine(t, 4, "tools/call", map[string]any{
		"name":      "list_apps",
		"arguments": map[string]any{},
	}))
	if len(resps) != 1 {
		t.Fatalf("expected 1 response")
	}
	res := resps[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Errorf("expected isError=true on tool failure, got %v", res)
	}
	// The model needs to *see* the error to react to it. Confirm the message
	// makes it into the content.
	content := res["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "boom") {
		t.Errorf("tool error message missing: %s", text)
	}
}

func TestMCP_DispatchUnknownTool(t *testing.T) {
	resps := runServer(t, nil, rpcLine(t, 5, "tools/call", map[string]any{
		"name":      "no_such_tool",
		"arguments": map[string]any{},
	}))
	res := resps[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Errorf("expected isError=true for unknown tool")
	}
}

func TestMCP_GetAppRequiresID(t *testing.T) {
	api, srv := fakeAPI(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("API should not be hit when validation fails: %s", r.URL.Path)
	})
	defer srv.Close()

	resps := runServer(t, api, rpcLine(t, 6, "tools/call", map[string]any{
		"name":      "get_app",
		"arguments": map[string]any{}, // missing id
	}))
	res := resps[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Errorf("expected validation error for missing id")
	}
}

func TestMCP_Whoami(t *testing.T) {
	api, srv := fakeAPI(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/me" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":42,"email":"user@example.com","name":"Alice","is_system_admin":true}`))
	})
	defer srv.Close()

	resps := runServer(t, api, rpcLine(t, 1, "tools/call", map[string]any{
		"name":      "whoami",
		"arguments": map[string]any{},
	}))
	res := resps[0]["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("unexpected error: %v", res)
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	for _, want := range []string{"#42 Alice", "user@example.com", "system_admin", "team_id=7"} {
		if !strings.Contains(text, want) {
			t.Errorf("whoami text missing %q\nfull: %s", want, text)
		}
	}
}

func TestMCP_SetEnvVarDefaultsRuntime(t *testing.T) {
	var gotPath, gotBody string
	api, srv := fakeAPI(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		gotBody = buf.String()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":99,"key":"DATABASE_URL"}`))
	})
	defer srv.Close()

	// Caller did NOT set is_runtime — the tool must default it to true so
	// the var actually reaches the running container.
	resps := runServer(t, api, rpcLine(t, 1, "tools/call", map[string]any{
		"name": "set_env_var",
		"arguments": map[string]any{
			"app_id": 7,
			"key":    "DATABASE_URL",
			"value":  "postgres://...",
		},
	}))
	res := resps[0]["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("unexpected error: %v", res)
	}
	if gotPath != "/api/v1/applications/7/env-vars" {
		t.Errorf("path: %s", gotPath)
	}
	if !strings.Contains(gotBody, `"is_runtime":true`) {
		t.Errorf("is_runtime should default to true; body=%s", gotBody)
	}
	if !strings.Contains(gotBody, `"key":"DATABASE_URL"`) {
		t.Errorf("body missing key: %s", gotBody)
	}
}

func TestMCP_TailAppLogsClampsLines(t *testing.T) {
	var gotURL string
	api, srv := fakeAPI(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"logs":"line1\nline2\n"}`))
	})
	defer srv.Close()

	resps := runServer(t, api, rpcLine(t, 1, "tools/call", map[string]any{
		"name": "tail_app_logs",
		"arguments": map[string]any{
			"app_id": 7,
			"lines":  500,
		},
	}))
	res := resps[0]["result"].(map[string]any)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "line1") {
		t.Errorf("expected log content, got: %s", text)
	}
	if !strings.Contains(gotURL, "lines=500") {
		t.Errorf("expected lines=500 in URL, got %s", gotURL)
	}
}

func TestMCP_DeleteSQLitePassesQueryParam(t *testing.T) {
	cases := []struct {
		name         string
		deleteVolume bool
		wantQuery    string
	}{
		{"preserve volume by default", false, ""},
		{"opt-in to wipe volume", true, "delete_volume=true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotURL string
			api, srv := fakeAPI(func(w http.ResponseWriter, r *http.Request) {
				gotURL = r.URL.String()
				w.WriteHeader(http.StatusNoContent)
			})
			defer srv.Close()

			args := map[string]any{"id": 11}
			if tc.deleteVolume {
				args["delete_volume"] = true
			}
			runServer(t, api, rpcLine(t, 1, "tools/call", map[string]any{
				"name":      "delete_sqlite_resource",
				"arguments": args,
			}))
			if tc.wantQuery != "" && !strings.Contains(gotURL, tc.wantQuery) {
				t.Errorf("expected URL to contain %q; got %s", tc.wantQuery, gotURL)
			}
			if tc.wantQuery == "" && strings.Contains(gotURL, "delete_volume=true") {
				t.Errorf("delete_volume should default to false; got %s", gotURL)
			}
		})
	}
}

func TestMCP_CreateSQLitePostsRightShape(t *testing.T) {
	var gotPath, gotBody string
	api, srv := fakeAPI(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		gotBody = buf.String()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":11,"name":"primary","application_id":7,"file_path":"/data/primary.db","env_var_key":"DATABASE_URL","connection_string":"sqlite:///data/primary.db","status":"active"}`))
	})
	defer srv.Close()

	resps := runServer(t, api, rpcLine(t, 7, "tools/call", map[string]any{
		"name": "create_sqlite_resource",
		"arguments": map[string]any{
			"name":           "primary",
			"environment_id": 3,
			"application_id": 7,
		},
	}))
	res := resps[0]["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("unexpected error: %v", res)
	}
	if gotPath != "/api/v1/sqlite" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if !strings.Contains(gotBody, `"name":"primary"`) || !strings.Contains(gotBody, `"application_id":7`) {
		t.Errorf("body missing fields: %s", gotBody)
	}
}
