// myserver env-vars — application-scoped environment variable management.
//
// Named cmd_envvars.go (not cmd_env.go) to avoid colliding with the
// existing cmd_env.go which manages ENVIRONMENTS (the project/env
// hierarchy thing). Different concept, same first three letters —
// the registered command is `env-vars` to keep it unambiguous on
// the command line.

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

func runEnvVars(args []string) error {
	if len(args) == 0 {
		envVarsUsage()
		return fmt.Errorf("no subcommand specified")
	}
	switch args[0] {
	case "list", "ls":
		return runEnvVarsList(args[1:])
	case "set":
		return runEnvVarsSet(args[1:])
	case "delete", "rm", "remove":
		return runEnvVarsDelete(args[1:])
	case "-h", "--help", "help":
		envVarsUsage()
		return nil
	default:
		envVarsUsage()
		return fmt.Errorf("unknown env-vars subcommand %q", args[0])
	}
}

func envVarsUsage() {
	fmt.Fprintln(os.Stderr, "Usage: myserver env-vars <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  list     List env vars on an app (values redacted for secrets).")
	fmt.Fprintln(os.Stderr, "  set      Create or replace an env var on an app.")
	fmt.Fprintln(os.Stderr, "  delete   Remove an env var by id.")
}

func runEnvVarsList(args []string) error {
	fs := flag.NewFlagSet("env-vars list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to ./myserver.json or your only team)")
	appID := fs.Int64("app", 0, "application id (defaults to ./myserver.json)")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	api, appResolved, err := resolveAppTarget(*teamID, *appID, *apiURL)
	if err != nil {
		return err
	}
	vars, err := api.listAppEnvVars(appResolved)
	if err != nil {
		return fmt.Errorf("list env vars: %w", err)
	}
	if len(vars) == 0 {
		fmt.Fprintln(os.Stderr, "(no env vars)")
		return nil
	}
	for _, v := range vars {
		flags := []string{}
		if v.IsLiteral {
			flags = append(flags, "literal")
		}
		if v.IsRuntime {
			flags = append(flags, "runtime")
		}
		if v.IsBuildtime {
			flags = append(flags, "buildtime")
		}
		val := v.Value
		if !v.IsLiteral && val != "" {
			val = "(encrypted)"
		}
		fmt.Printf("%d\t%s\t%s\t[%s]\n", v.ID, v.Key, val, strings.Join(flags, ","))
	}
	return nil
}

// runEnvVarsSet handles `myserver env-vars set`. Defaults are
// runtime-only + encrypted (is_literal=false). Pass --literal for
// non-secret values (URLs, log levels) — those stay plaintext in
// the DB and are visible in list output.
func runEnvVarsSet(args []string) error {
	fs := flag.NewFlagSet("env-vars set", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to ./myserver.json or your only team)")
	appID := fs.Int64("app", 0, "application id (defaults to ./myserver.json)")
	key := fs.String("key", "", "env var key (required, e.g. API_KEY)")
	value := fs.String("value", "", "env var value (required)")
	literal := fs.Bool("literal", false, "store plaintext (not encrypted) — use for URLs, log levels, non-secrets")
	runtime := fs.Bool("runtime", true, "available at container runtime (default true)")
	buildtime := fs.Bool("buildtime", false, "available during build (default false)")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver env-vars set --key=<KEY> --value=<value> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Required:")
		fmt.Fprintln(os.Stderr, "  --key=<KEY>        env var name (typically UPPERCASE)")
		fmt.Fprintln(os.Stderr, "  --value=<value>    the value (encrypted at rest unless --literal)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Optional:")
		fmt.Fprintln(os.Stderr, "  --literal          store plaintext (only for non-secret values)")
		fmt.Fprintln(os.Stderr, "  --buildtime        make available during builds (default false)")
		fmt.Fprintln(os.Stderr, "  --runtime=false    NOT available at container runtime (rare)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Important: don't set MYSERVER_API_URL, MYSERVER_APP_ID, MYSERVER_APP_TOKEN,")
		fmt.Fprintln(os.Stderr, "LOG_INGEST_URL, LOG_INGEST_TOKEN manually — the deploy pipeline auto-injects them.")
		fmt.Fprintln(os.Stderr, "Setting DATABASE_URL manually IS expected when you have a SQLite resource.")
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(*key) == "" {
		fs.Usage()
		return fmt.Errorf("--key is required")
	}
	api, appResolved, err := resolveAppTarget(*teamID, *appID, *apiURL)
	if err != nil {
		return err
	}
	ev, err := api.createAppEnvVar(appResolved, CreateEnvVarRequest{
		Key:         strings.TrimSpace(*key),
		Value:       *value,
		IsLiteral:   *literal,
		IsRuntime:   *runtime,
		IsBuildtime: *buildtime,
	})
	if err != nil {
		return fmt.Errorf("set env var: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Set %s on app %d (id %d)\n", ev.Key, appResolved, ev.ID)
	if *runtime {
		fmt.Fprintln(os.Stderr, "  takes effect on next deploy")
	}
	return nil
}

func runEnvVarsDelete(args []string) error {
	fs := flag.NewFlagSet("env-vars delete", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to your only team)")
	id := fs.Int64("id", 0, "env var id (required, see `myserver env-vars list`)")
	yes := fs.Bool("yes", false, "skip confirmation prompt")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *id == 0 {
		return fmt.Errorf("--id is required")
	}
	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}
	if !*yes {
		if !promptConfirm(fmt.Sprintf("Delete env var id %d? Type 'yes' to confirm: ", *id), "yes") {
			return fmt.Errorf("aborted")
		}
	}
	if err := api.deleteEnvVar(*id); err != nil {
		return fmt.Errorf("delete env var: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Deleted env var id %d\n", *id)
	return nil
}

// resolveAppTarget centralises the bound-dir-or-explicit-id resolution
// every per-app subcommand shares (env-vars, domains, db actions on app).
// Returns the api client + the app_id ready for use.
func resolveAppTarget(teamID, appID int64, apiURL string) (*apiClient, int64, error) {
	if appID == 0 || teamID == 0 {
		if pc, err := loadProjectConfig(); err == nil && pc != nil {
			if appID == 0 {
				appID = pc.AppID
			}
			if teamID == 0 {
				teamID = pc.TeamID
			}
		}
	}
	if appID == 0 {
		return nil, 0, fmt.Errorf("--app is required (or run from a directory with myserver.json)")
	}
	api, _, err := resolveTeamAPI(teamID, apiURL)
	if err != nil {
		return nil, 0, err
	}
	return api, appID, nil
}
