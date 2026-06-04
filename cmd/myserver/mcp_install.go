// myserver-cli mcp install — one-shot MCP wire-up for AI editors.
//
// Why this exists: every MCP-aware editor stores its server registry in a
// different JSON file under a different conventionally-named directory.
// Customers shouldn't have to learn that map of editor → path; they should
// run one command. This file is that command.
//
// What we DON'T do: nothing fancy. Each install is "read JSON, splice in
// our `myserver` entry under mcpServers, write JSON back". The merge is
// idempotent (safe to re-run) and preserves any other servers the user
// has already configured.

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// mcpServerEntry is the single object we splice into each editor's config.
// The format is identical across Claude Desktop, Claude Code, and Cursor.
type mcpServerEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// mcpTarget is one editor we know how to install into.
type mcpTarget struct {
	id     string                 // CLI flag value: "claude-desktop", "claude-code", "cursor"
	label  string                 // user-facing name in messages
	path   func() (string, error) // returns the config file path for this OS
	mutate func(file string, e mcpServerEntry, remove bool) error
}

func mcpTargets() []mcpTarget {
	return []mcpTarget{
		{
			id:     "claude-desktop",
			label:  "Claude Desktop",
			path:   claudeDesktopConfigPath,
			mutate: mutateMCPServersFile,
		},
		{
			id:     "claude-code",
			label:  "Claude Code",
			path:   claudeCodeConfigPath,
			mutate: mutateMCPServersFile,
		},
		{
			id:     "cursor",
			label:  "Cursor",
			path:   cursorConfigPath,
			mutate: mutateMCPServersFile,
		},
	}
}

func runMCPInstall(args []string) error {
	fs := flag.NewFlagSet("mcp install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	target := fs.String("target", "", "specific editor to install into (claude-desktop|claude-code|cursor); installs into all detected editors when empty")
	dryRun := fs.Bool("dry-run", false, "show what would change without writing files")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver mcp install [--target=<editor>] [--dry-run]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Wires the myserver MCP server into one or more AI editor configs.")
		fmt.Fprintln(os.Stderr, "  With no --target, every editor whose config dir exists is updated.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Re-running is safe: an existing 'myserver' entry is replaced in place;")
		fmt.Fprintln(os.Stderr, "  other servers are preserved untouched.")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	bin, err := mcpResolveBinaryPath()
	if err != nil {
		return err
	}
	entry := mcpServerEntry{Command: bin, Args: []string{"mcp"}}

	return forEachTarget(*target, *dryRun, func(t mcpTarget, path string) error {
		if *dryRun {
			fmt.Fprintf(os.Stderr, "  [dry-run] would update %s (%s)\n", t.label, path)
			return nil
		}
		if err := t.mutate(path, entry, false); err != nil {
			return fmt.Errorf("%s: %w", t.label, err)
		}
		fmt.Fprintf(os.Stderr, "  ✓ %s — %s\n", t.label, path)
		return nil
	})
}

func runMCPUninstall(args []string) error {
	fs := flag.NewFlagSet("mcp uninstall", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	target := fs.String("target", "", "specific editor to uninstall from (claude-desktop|claude-code|cursor); uninstalls from all when empty")
	dryRun := fs.Bool("dry-run", false, "show what would change without writing files")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	return forEachTarget(*target, *dryRun, func(t mcpTarget, path string) error {
		if *dryRun {
			fmt.Fprintf(os.Stderr, "  [dry-run] would remove from %s (%s)\n", t.label, path)
			return nil
		}
		if err := t.mutate(path, mcpServerEntry{}, true); err != nil {
			return fmt.Errorf("%s: %w", t.label, err)
		}
		fmt.Fprintf(os.Stderr, "  ✓ %s — removed from %s\n", t.label, path)
		return nil
	})
}

func runMCPList(args []string) error {
	fs := flag.NewFlagSet("mcp list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("mcp list does not accept positional arguments")
	}

	fmt.Fprintln(os.Stderr, "MyServer MCP integrations:")
	for _, t := range mcpTargets() {
		path, err := t.path()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s — unavailable: %v\n", t.label, err)
			continue
		}
		status := mcpConfigStatus(path)
		switch {
		case status.installed:
			fmt.Fprintf(os.Stderr, "  ✓ %s — installed (%s)\n", t.label, path)
			if status.command != "" {
				fmt.Fprintf(os.Stderr, "      command: %s\n", status.command)
			}
		case status.exists:
			fmt.Fprintf(os.Stderr, "  - %s — config exists, myserver not installed (%s)\n", t.label, path)
		default:
			fmt.Fprintf(os.Stderr, "  - %s — not installed (%s)\n", t.label, path)
		}
	}
	return nil
}

type mcpInstallStatus struct {
	exists    bool
	installed bool
	command   string
}

func mcpConfigStatus(file string) mcpInstallStatus {
	data, err := os.ReadFile(file)
	if err != nil {
		return mcpInstallStatus{exists: false}
	}
	status := mcpInstallStatus{exists: true}

	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return status
	}
	servers, _ := root["mcpServers"].(map[string]any)
	myserver, _ := servers["myserver"].(map[string]any)
	if myserver == nil {
		return status
	}
	status.installed = true
	status.command, _ = myserver["command"].(string)
	return status
}

// forEachTarget runs fn for each target whose config path is known and either
// already exists, or — when --target was passed explicitly — whose parent dir
// can be created. Auto-discovery (no --target) silently skips editors whose
// config file doesn't exist yet, so we don't litter Cursor configs onto
// machines that have only Claude installed, for example.
func forEachTarget(targetID string, dryRun bool, fn func(t mcpTarget, path string) error) error {
	all := mcpTargets()
	var wanted []mcpTarget
	if targetID != "" {
		for _, t := range all {
			if t.id == targetID {
				wanted = []mcpTarget{t}
				break
			}
		}
		if len(wanted) == 0 {
			ids := make([]string, len(all))
			for i, t := range all {
				ids[i] = t.id
			}
			sort.Strings(ids)
			return fmt.Errorf("unknown --target %q (known: %s)", targetID, strings.Join(ids, ", "))
		}
	} else {
		wanted = all
	}

	var anyApplied, anySkipped int
	var firstErr error
	verb := "Installing"
	if dryRun {
		verb = "Previewing"
	}
	fmt.Fprintf(os.Stderr, "%s myserver MCP integration:\n", verb)

	for _, t := range wanted {
		path, err := t.path()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s — %v\n", t.label, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		// In auto-discovery mode, only touch editors that exist on disk.
		// In explicit --target mode, create the parent dir if missing.
		if targetID == "" {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				anySkipped++
				continue
			}
		}
		if err := fn(t, path); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s — %v\n", t.label, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		anyApplied++
	}

	if anyApplied == 0 && targetID == "" {
		if !dryRun && isInteractiveTerminal() {
			chosen, ok, err := promptMCPInstallTargets(os.Stdin, os.Stderr, all)
			if err != nil {
				return err
			}
			if ok {
				return forTargetIDs(chosen, dryRun, fn)
			}
		}
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "No supported MCP editors detected. Pass --target=<editor> to force install,")
		fmt.Fprintln(os.Stderr, "or install one of: Claude Desktop, Claude Code, Cursor.")
		return nil
	}
	if !dryRun && anyApplied > 0 {
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Restart your editor to load the new MCP server.")
	}
	return firstErr
}

func forTargetIDs(targetIDs []string, dryRun bool, fn func(t mcpTarget, path string) error) error {
	all := mcpTargets()
	var wanted []mcpTarget
	for _, id := range targetIDs {
		for _, t := range all {
			if t.id == id {
				wanted = append(wanted, t)
				break
			}
		}
	}

	var firstErr error
	verb := "Installing"
	if dryRun {
		verb = "Previewing"
	}
	fmt.Fprintf(os.Stderr, "%s myserver MCP integration:\n", verb)

	var anyApplied int
	for _, t := range wanted {
		path, err := t.path()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s — %v\n", t.label, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := fn(t, path); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s — %v\n", t.label, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		anyApplied++
	}

	if !dryRun && anyApplied > 0 {
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Restart your editor to load the new MCP server.")
	}
	return firstErr
}

func isInteractiveTerminal() bool {
	in, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	out, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return (in.Mode()&os.ModeCharDevice) != 0 && (out.Mode()&os.ModeCharDevice) != 0
}

func promptMCPInstallTargets(in io.Reader, out io.Writer, targets []mcpTarget) ([]string, bool, error) {
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "No supported MCP editor config was detected.")
	fmt.Fprintln(out, "Choose where to install the MyServer MCP server:")
	for i, t := range targets {
		fmt.Fprintf(out, "  %d) %s (%s)\n", i+1, t.label, t.id)
	}
	fmt.Fprintln(out, "  all) install into every target")
	fmt.Fprintln(out, "  Enter) skip for now")
	fmt.Fprint(out, "Install targets (comma or space separated): ")

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	answer := strings.TrimSpace(line)
	if answer == "" {
		return nil, false, nil
	}

	if strings.EqualFold(answer, "all") {
		ids := make([]string, len(targets))
		for i, t := range targets {
			ids[i] = t.id
		}
		return ids, true, nil
	}

	byToken := map[string]string{}
	for i, t := range targets {
		byToken[fmt.Sprintf("%d", i+1)] = t.id
		byToken[strings.ToLower(t.id)] = t.id
	}

	seen := map[string]bool{}
	var selected []string
	for _, token := range strings.FieldsFunc(answer, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	}) {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		id, ok := byToken[strings.ToLower(token)]
		if !ok {
			ids := make([]string, len(targets))
			for i, t := range targets {
				ids[i] = t.id
			}
			sort.Strings(ids)
			return nil, false, fmt.Errorf("unknown install target %q (known: %s, all)", token, strings.Join(ids, ", "))
		}
		if !seen[id] {
			seen[id] = true
			selected = append(selected, id)
		}
	}
	if len(selected) == 0 {
		return nil, false, nil
	}
	return selected, true, nil
}

// mcpResolveBinaryPath returns the absolute path of the running myserver
// binary. MCP clients launched from GUIs (Finder/Spotlight on macOS,
// Explorer on Windows) inherit a minimal $PATH, so a bare command name
// often won't resolve. Embedding the absolute path makes the config robust.
func mcpResolveBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err == nil {
		// Resolve symlinks so we don't accidentally embed a path that
		// points at a stale build via a homebrew shim.
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			return resolved, nil
		}
		return exe, nil
	}
	// Fallback: lookup on PATH. Best-effort — better than failing outright.
	if path, err := exec.LookPath("myserver"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("could not locate the myserver binary on disk")
}

// ── Per-editor config file resolution ─────────────────────────────────────

func claudeDesktopConfigPath() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json"), nil
	case "windows":
		appdata := os.Getenv("APPDATA")
		if appdata == "" {
			return "", fmt.Errorf("APPDATA not set")
		}
		return filepath.Join(appdata, "Claude", "claude_desktop_config.json"), nil
	default:
		// Claude Desktop for Linux follows the XDG-config convention.
		cfg, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(cfg, "Claude", "claude_desktop_config.json"), nil
	}
}

func claudeCodeConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude.json"), nil
}

func cursorConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cursor", "mcp.json"), nil
}

// ── Idempotent JSON merge ─────────────────────────────────────────────────

// mutateMCPServersFile reads file, ensures `mcpServers.myserver` matches
// entry (or is removed when remove=true), writes file back. Preserves all
// other keys and other MCP servers.
func mutateMCPServersFile(file string, entry mcpServerEntry, remove bool) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	root := map[string]any{}
	if data, err := os.ReadFile(file); err == nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("existing config is not valid JSON (%w) — refusing to clobber. Edit %s by hand or delete it first.", err, file)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read config: %w", err)
	}

	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}

	if remove {
		delete(servers, "myserver")
	} else {
		// Marshal/unmarshal through any so the resulting JSON has the same
		// shape (omitempty for empty Args, env, etc.) as if the user had
		// written it by hand.
		raw, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		var asAny any
		if err := json.Unmarshal(raw, &asAny); err != nil {
			return err
		}
		servers["myserver"] = asAny
	}
	root["mcpServers"] = servers

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	out = append(out, '\n')
	// Write atomically: temp + rename. Avoids corrupting the editor config
	// if the process is killed mid-write.
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
