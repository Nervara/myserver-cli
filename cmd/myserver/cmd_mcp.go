// myserver-cli mcp — Model Context Protocol server over stdio.
//
// Lets any MCP-aware AI client (Claude Desktop, Cursor, Cline, Claude Code,
// Continue, Zed, etc.) drive a myserver instance via tool calls.
//
// Wire-up example for Claude Desktop (~/Library/Application Support/Claude/
// claude_desktop_config.json):
//
//	{
//	  "mcpServers": {
//	    "myserver": {
//	      "command": "/usr/local/bin/myserver-cli",
//	      "args": ["mcp"]
//	    }
//	  }
//	}
//
// Auth: reuses ~/.myserver/credentials.json from `myserver login`. For
// headless / CI, set MYSERVER_API_URL + MYSERVER_TOKEN (and optionally
// MYSERVER_TEAM_ID) instead. For first-user bootstrap, MYSERVER_API_URL alone
// is enough for unauthenticated tools such as register_user.

package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// MCP / JSON-RPC error codes (subset of the spec — only what we actually emit).
const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
)

const mcpProtocolVersion = "2024-11-05"

// rpcMessage is the union of request, response, and notification — IDs and
// optional fields differentiate them.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// runMCP dispatches `myserver mcp [install|uninstall|...]`. With no
// subcommand it runs the JSON-RPC server over stdio.
func runMCP(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "install":
			return runMCPInstall(args[1:])
		case "uninstall":
			return runMCPUninstall(args[1:])
		case "list":
			return runMCPList(args[1:])
		case "-h", "--help", "help":
			mcpUsage()
			return nil
		default:
			return fmt.Errorf("unknown mcp subcommand %q (known: install, uninstall, list)", args[0])
		}
	}

	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = mcpUsage
	if err := fs.Parse(args); err != nil {
		return err
	}

	api, err := mcpResolveAPIClient()
	if err != nil {
		return err
	}

	srv := &mcpServer{api: api, out: os.Stdout, log: os.Stderr}
	return srv.serve(os.Stdin)
}

func mcpUsage() {
	fmt.Fprintln(os.Stderr, "Usage: myserver mcp [subcommand]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  (no args)   Run an MCP server over stdio for an AI client.")
	fmt.Fprintln(os.Stderr, "  install     Wire myserver into Claude Desktop / Claude Code / Cursor.")
	fmt.Fprintln(os.Stderr, "  list        Show MCP install status for supported editors.")
	fmt.Fprintln(os.Stderr, "  uninstall   Remove myserver entries from MCP client configs.")
}

// mcpResolveAPIClient picks credentials in priority order:
//  1. MYSERVER_API_URL + MYSERVER_TOKEN env vars (headless mode)
//  2. ~/.myserver/credentials.json (interactive `myserver login` flow)
//  3. MYSERVER_API_URL only (bootstrap mode for unauthenticated tools)
//
// Team ID resolution, in order:
//  1. MYSERVER_TEAM_ID env var
//  2. ./myserver.json project config (if launched from a project dir)
//  3. First team returned by /api/v1/teams (auto-detect, with a stderr note)
func mcpResolveAPIClient() (*apiClient, error) {
	var creds *Credentials
	if url, tok := os.Getenv("MYSERVER_API_URL"), os.Getenv("MYSERVER_TOKEN"); url != "" && tok != "" {
		creds = &Credentials{APIURL: url, Token: tok}
	} else {
		c, err := loadCredentials()
		if err != nil {
			if url := os.Getenv("MYSERVER_API_URL"); url != "" {
				return newAPI(&Credentials{APIURL: url}, 0), nil
			}
			return nil, fmt.Errorf("mcp: %w (or set MYSERVER_API_URL + MYSERVER_TOKEN)", err)
		}
		creds = c
	}

	teamID := mcpEnvInt64("MYSERVER_TEAM_ID")
	if teamID == 0 {
		if pc, err := loadProjectConfig(); err == nil && pc != nil {
			teamID = pc.TeamID
		}
	}
	if teamID == 0 {
		// Auto-detect: first team. Build a temp client for the unscoped
		// /teams call (auth-only endpoint, ignores X-Team-ID).
		tmp := newAPI(creds, 0)
		teams, err := tmp.listTeams()
		if err == nil && len(teams) > 0 {
			teamID = teams[0].ID
			fmt.Fprintf(os.Stderr, "mcp: no MYSERVER_TEAM_ID set — defaulting to team %d (%q). Set MYSERVER_TEAM_ID to override.\n", teamID, teams[0].Name)
		}
	}
	return newAPI(creds, teamID), nil
}

func mcpEnvInt64(key string) int64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// mcpServer is a minimal MCP server: handshake + tools/list + tools/call.
// Resources, prompts, and sampling are not implemented for the MVP.
type mcpServer struct {
	api         *apiClient
	out         io.Writer
	log         io.Writer
	initialized bool
}

func (s *mcpServer) serve(in io.Reader) error {
	scanner := bufio.NewScanner(in)
	// Allow large request bodies (file lists, log dumps, etc.).
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		s.handleLine(line)
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return fmt.Errorf("read stdin: %w", err)
	}
	return nil
}

func (s *mcpServer) handleLine(line string) {
	var msg rpcMessage
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		s.writeError(nil, rpcParseError, "parse error: "+err.Error())
		return
	}
	if msg.JSONRPC != "2.0" {
		s.writeError(msg.ID, rpcInvalidRequest, "jsonrpc must be \"2.0\"")
		return
	}

	switch msg.Method {
	case "initialize":
		s.handleInitialize(msg)
	case "notifications/initialized":
		// Notification — no response. Client signals it's ready.
		s.initialized = true
	case "tools/list":
		s.handleToolsList(msg)
	case "tools/call":
		s.handleToolsCall(msg)
	case "ping":
		s.writeResult(msg.ID, map[string]any{})
	default:
		// Notifications have no ID and expect no response.
		if len(msg.ID) == 0 {
			return
		}
		s.writeError(msg.ID, rpcMethodNotFound, "method not implemented: "+msg.Method)
	}
}

func (s *mcpServer) handleInitialize(req rpcMessage) {
	s.writeResult(req.ID, map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "myserver",
			"version": cliVersion,
		},
	})
}

func (s *mcpServer) handleToolsList(req rpcMessage) {
	s.writeResult(req.ID, map[string]any{"tools": mcpToolDescriptors()})
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *mcpServer) handleToolsCall(req rpcMessage) {
	var p toolCallParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.writeError(req.ID, rpcInvalidParams, "invalid tools/call params: "+err.Error())
		return
	}

	text, err := dispatchTool(s.api, p.Name, p.Arguments)
	if err != nil {
		// Tool errors are reported in the result envelope (isError=true) so
		// the client can show them to the model. JSON-RPC errors are reserved
		// for protocol-level failures.
		s.writeResult(req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
			"isError": true,
		})
		return
	}
	s.writeResult(req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	})
}

// --- write helpers ----------------------------------------------------------

func (s *mcpServer) writeResult(id json.RawMessage, result any) {
	s.writeMessage(rpcMessage{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *mcpServer) writeError(id json.RawMessage, code int, msg string) {
	s.writeMessage(rpcMessage{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func (s *mcpServer) writeMessage(m rpcMessage) {
	buf, err := json.Marshal(m)
	if err != nil {
		fmt.Fprintf(s.log, "mcp: marshal response: %v\n", err)
		return
	}
	buf = append(buf, '\n')
	if _, err := s.out.Write(buf); err != nil {
		fmt.Fprintf(s.log, "mcp: write response: %v\n", err)
	}
}
