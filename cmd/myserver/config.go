package main

// Two config files:
//
//   ~/.myserver/credentials.json    — written by `myserver login`. Stores the
//                                     API URL and the bearer token. Mode 0600.
//   ./myserver.json                 — written by `myserver init`. Stores the
//                                     team/app/server IDs + deploy hints. Safe
//                                     to commit (no secrets).
//
// Both are JSON (zero-dep). TOML would be nicer to hand-edit but isn't worth
// pulling in pelletier just for two small files.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Credentials persists per-machine across CLI invocations. Don't commit.
type Credentials struct {
	APIURL        string `json:"api_url"`
	Token         string `json:"token"`
	Email         string `json:"email,omitempty"`           // for `status` to show whose creds these are
	CurrentTeamID int64  `json:"current_team_id,omitempty"` // last team selected by the CLI
}

// ProjectConfig persists per-repo. Commit it so teammates `myserver up` the
// same target.
//
// Phase 1 onwards: builds run on the build server (set by app.destination_id
// on the SaaS side), uploads go through the API. The CLI no longer needs
// SSH/registry hints — those fields existed in the bash-shim era.
type ProjectConfig struct {
	TeamID       int64  `json:"team_id"`
	ServerID     int64  `json:"server_id"`
	AppID        int64  `json:"app_id"`
	AppName      string `json:"app_name,omitempty"`
	BuildPack    string `json:"build_pack"`
	PortsExposes string `json:"ports_exposes,omitempty"`
	FQDN         string `json:"fqdn,omitempty"`
	IgnoreFile   string `json:"ignore_file,omitempty"` // default ".myserverignore" then ".dockerignore"
	ImageName    string `json:"image_name,omitempty"`  // default = AppName lowercased
}

// errNotLoggedIn is returned by loadCredentials when the file is missing.
var errNotLoggedIn = errors.New("not logged in (run `myserver login`)")

// errNoProject is returned by loadProjectConfig when myserver.json is missing.
var errNoProject = errors.New("no myserver.json in this directory (run `myserver init`)")

func credentialsFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	return filepath.Join(home, credentialsPath), nil
}

func loadCredentials() (*Credentials, error) {
	path, err := credentialsFile()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNotLoggedIn
	}
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	if c.Token == "" {
		return nil, errNotLoggedIn
	}
	return &c, nil
}

func saveCredentials(c *Credentials) error {
	path, err := credentialsFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create credentials dir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// 0600 — only the owning user can read the token.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	return nil
}

func loadProjectConfig() (*ProjectConfig, error) {
	data, err := os.ReadFile(projectConfigFn)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNoProject
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", projectConfigFn, err)
	}
	var p ProjectConfig
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse %s: %w", projectConfigFn, err)
	}
	return &p, nil
}

func saveProjectConfig(p *ProjectConfig) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(projectConfigFn, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", projectConfigFn, err)
	}
	return nil
}
