package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMutateMCPServersFile_AddsEntryToFreshFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.json")

	if err := mutateMCPServersFile(file, mcpServerEntry{Command: "/bin/myserver", Args: []string{"mcp"}}, false); err != nil {
		t.Fatalf("mutate: %v", err)
	}

	got := readConfig(t, file)
	servers := got["mcpServers"].(map[string]any)
	ms := servers["myserver"].(map[string]any)
	if ms["command"] != "/bin/myserver" {
		t.Fatalf("command = %v", ms["command"])
	}
	args := ms["args"].([]any)
	if len(args) != 1 || args[0] != "mcp" {
		t.Fatalf("args = %v", args)
	}
}

func TestMutateMCPServersFile_PreservesOtherServers(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.json")

	// Pre-existing config with another MCP server and a sibling top-level key.
	seed := map[string]any{
		"theme": "dark",
		"mcpServers": map[string]any{
			"github": map[string]any{
				"command": "/usr/bin/gh-mcp",
				"args":    []any{"serve"},
			},
		},
	}
	writeConfig(t, file, seed)

	if err := mutateMCPServersFile(file, mcpServerEntry{Command: "/bin/myserver", Args: []string{"mcp"}}, false); err != nil {
		t.Fatalf("mutate: %v", err)
	}

	got := readConfig(t, file)
	if got["theme"] != "dark" {
		t.Fatalf("sibling key clobbered: %v", got["theme"])
	}
	servers := got["mcpServers"].(map[string]any)
	if _, ok := servers["github"]; !ok {
		t.Fatal("github MCP server was removed")
	}
	if _, ok := servers["myserver"]; !ok {
		t.Fatal("myserver MCP server was not added")
	}
}

func TestMutateMCPServersFile_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.json")

	entry := mcpServerEntry{Command: "/bin/myserver", Args: []string{"mcp"}}
	for i := 0; i < 3; i++ {
		if err := mutateMCPServersFile(file, entry, false); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}
	got := readConfig(t, file)
	servers := got["mcpServers"].(map[string]any)
	// Still exactly one entry — re-runs replace, not append.
	if len(servers) != 1 {
		t.Fatalf("expected 1 server after 3 idempotent installs, got %d", len(servers))
	}
}

func TestMutateMCPServersFile_RemoveDeletesEntryButKeepsOthers(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.json")
	seed := map[string]any{
		"mcpServers": map[string]any{
			"myserver": map[string]any{"command": "/bin/myserver"},
			"github":   map[string]any{"command": "/usr/bin/gh-mcp"},
		},
	}
	writeConfig(t, file, seed)

	if err := mutateMCPServersFile(file, mcpServerEntry{}, true); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	got := readConfig(t, file)
	servers := got["mcpServers"].(map[string]any)
	if _, ok := servers["myserver"]; ok {
		t.Fatal("myserver entry should have been removed")
	}
	if _, ok := servers["github"]; !ok {
		t.Fatal("github entry should still be present")
	}
}

func TestMutateMCPServersFile_RefusesInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.json")
	if err := os.WriteFile(file, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	err := mutateMCPServersFile(file, mcpServerEntry{Command: "/bin/myserver"}, false)
	if err == nil {
		t.Fatal("expected error on invalid existing JSON, got nil")
	}
	// The original file should be untouched.
	body, _ := os.ReadFile(file)
	if string(body) != "{ this is not json" {
		t.Fatalf("file was modified despite error: %q", body)
	}
}

func TestPromptMCPInstallTarget_SelectsNumberedTarget(t *testing.T) {
	targets := []mcpTarget{
		{id: "claude-desktop", label: "Claude Desktop"},
		{id: "claude-code", label: "Claude Code"},
		{id: "cursor", label: "Cursor"},
	}
	var out bytes.Buffer

	got, ok, err := promptMCPInstallTarget(strings.NewReader("2\n"), &out, targets)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if !ok {
		t.Fatal("expected target selection")
	}
	if got != "claude-code" {
		t.Fatalf("target = %q, want claude-code", got)
	}
	if !strings.Contains(out.String(), "1) Claude Desktop") ||
		!strings.Contains(out.String(), "2) Claude Code") ||
		!strings.Contains(out.String(), "3) Cursor") {
		t.Fatalf("menu missing targets:\n%s", out.String())
	}
}

func TestPromptMCPInstallTarget_SelectsTargetID(t *testing.T) {
	targets := []mcpTarget{
		{id: "claude-code", label: "Claude Code"},
		{id: "cursor", label: "Cursor"},
	}

	got, ok, err := promptMCPInstallTarget(strings.NewReader("cursor\n"), &bytes.Buffer{}, targets)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if !ok || got != "cursor" {
		t.Fatalf("selection = %q, %v; want cursor, true", got, ok)
	}
}

func TestPromptMCPInstallTarget_EnterSkips(t *testing.T) {
	got, ok, err := promptMCPInstallTarget(
		strings.NewReader("\n"),
		&bytes.Buffer{},
		[]mcpTarget{{id: "claude-code", label: "Claude Code"}},
	)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if ok || got != "" {
		t.Fatalf("selection = %q, %v; want empty, false", got, ok)
	}
}

func writeConfig(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}
