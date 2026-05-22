// myserver sqlite — managed SQLite resource subcommands.
//
// A "SQLite resource" attaches a persistent Docker volume to one app and
// injects an env var (default DATABASE_URL) pointing at a .db file inside
// it. The app stays single-replica; the volume survives redeploys.
//
// The HTTP and MCP surfaces have shipped for a while — this file is the
// CLI mirror so customers can wire SQLite without leaving the terminal.

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func runSqlite(args []string) error {
	if len(args) == 0 {
		sqliteUsage()
		return fmt.Errorf("no subcommand specified")
	}
	switch args[0] {
	case "list", "ls":
		return runSqliteList(args[1:])
	case "create":
		return runSqliteCreate(args[1:])
	case "get", "show":
		return runSqliteGet(args[1:])
	case "delete", "rm":
		return runSqliteDelete(args[1:])
	case "-h", "--help", "help":
		sqliteUsage()
		return nil
	default:
		sqliteUsage()
		return fmt.Errorf("unknown sqlite subcommand %q", args[0])
	}
}

func sqliteUsage() {
	fmt.Fprintln(os.Stderr, "Usage: myserver sqlite <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  list     List SQLite resources in the team.")
	fmt.Fprintln(os.Stderr, "  create   Attach a managed SQLite database to an app (volume + env var).")
	fmt.Fprintln(os.Stderr, "  get      Show one SQLite resource by id.")
	fmt.Fprintln(os.Stderr, "  delete   Detach a SQLite resource (use --delete-volume to also nuke data).")
}

func runSqliteList(args []string) error {
	fs := flag.NewFlagSet("sqlite list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to your only team, or prompts)")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}
	rs, err := api.listSqliteResources()
	if err != nil {
		return fmt.Errorf("list sqlite: %w", err)
	}
	if len(rs) == 0 {
		fmt.Fprintln(os.Stderr, "(no SQLite resources)")
		return nil
	}
	for _, r := range rs {
		fmt.Printf("%d\t%s\tapp=%d\t%s\tenv=%s\tstatus=%s\n",
			r.ID, r.Name, r.ApplicationID, r.FilePath, r.EnvVarKey, r.Status)
	}
	return nil
}

// runSqliteCreate handles `myserver sqlite create`. Two modes:
//
//   - Explicit: --app + --env + --name. Useful in scripts.
//   - Inferred: --name only when run inside a myserver.json-bound dir
//     (we pull app_id from the project config and resolve env from the
//     app). Mirrors how `myserver up` discovers its target.
//
// We always derive `environment_id` from the app server-side rather than
// trusting the caller, because that's the only ID the server actually
// uses for scoping — but we still let the user pass --env to short-
// circuit the extra round trip.
func runSqliteCreate(args []string) error {
	fs := flag.NewFlagSet("sqlite create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to ./myserver.json or your only team)")
	appID := fs.Int64("app", 0, "application id (defaults to ./myserver.json)")
	envID := fs.Int64("env", 0, "environment id (defaults: derived from --app)")
	name := fs.String("name", "", "resource name (required, e.g. primary)")
	filePath := fs.String("file", "", "absolute file path inside the container (default /data/<name>.db)")
	envVar := fs.String("env-var", "", "env var key injected into the app (default DATABASE_URL)")
	journal := fs.String("journal", "", "SQLite journal_mode pragma (default WAL)")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver sqlite create --name=<name> [--app=<id>] [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Required:")
		fmt.Fprintln(os.Stderr, "  --name=<name>      resource name (used in URLs and as default file name)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Inferred from ./myserver.json when omitted:")
		fmt.Fprintln(os.Stderr, "  --app=<id>         application id")
		fmt.Fprintln(os.Stderr, "  --env=<id>         environment id (else derived from --app)")
		fmt.Fprintln(os.Stderr, "  --team=<id>        team id")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Optional:")
		fmt.Fprintln(os.Stderr, "  --file=<path>      container path for the .db file (default /data/<name>.db)")
		fmt.Fprintln(os.Stderr, "  --env-var=<key>    env var key injected into the app (default DATABASE_URL)")
		fmt.Fprintln(os.Stderr, "  --journal=<mode>   journal_mode pragma (default WAL)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Examples:")
		fmt.Fprintln(os.Stderr, "  # from a bound directory")
		fmt.Fprintln(os.Stderr, "  myserver sqlite create --name=primary")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  # scripted, all explicit")
		fmt.Fprintln(os.Stderr, "  myserver sqlite create --app=42 --env=3 --name=primary \\")
		fmt.Fprintln(os.Stderr, "    --file=/data/primary.db --env-var=DATABASE_URL")
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(*name) == "" {
		fs.Usage()
		return fmt.Errorf("--name is required")
	}

	// Fall back to project config for any missing IDs — same pattern as `app update`.
	if *appID == 0 || *teamID == 0 {
		if pc, err := loadProjectConfig(); err == nil && pc != nil {
			if *appID == 0 {
				*appID = pc.AppID
			}
			if *teamID == 0 {
				*teamID = pc.TeamID
			}
		}
	}
	if *appID == 0 {
		return fmt.Errorf("--app is required (or run from a directory with myserver.json)")
	}

	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}

	// Derive env from app if the caller didn't pin one explicitly. Single
	// extra GET, but it spares the user a copy-paste from the web UI.
	if *envID == 0 {
		app, err := api.getApp(*appID)
		if err != nil {
			return fmt.Errorf("derive environment from app %d: %w", *appID, err)
		}
		*envID = app.EnvironmentID
		if *envID == 0 {
			return fmt.Errorf("app %d has no environment_id — pass --env explicitly", *appID)
		}
	}

	req := CreateSQLiteRequest{
		Name:          strings.TrimSpace(*name),
		EnvironmentID: *envID,
		ApplicationID: *appID,
		FilePath:      strings.TrimSpace(*filePath),
		EnvVarKey:     strings.TrimSpace(*envVar),
		PragmaJournal: strings.TrimSpace(*journal),
	}
	r, err := api.createSqliteResource(req)
	if err != nil {
		return fmt.Errorf("create sqlite: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Created SQLite resource %q (id %d) on app %d\n", r.Name, r.ID, r.ApplicationID)
	fmt.Fprintf(os.Stderr, "  file:    %s\n", r.FilePath)
	fmt.Fprintf(os.Stderr, "  env_var: %s=%s\n", r.EnvVarKey, r.ConnectionString)
	if r.PragmaJournal != "" {
		fmt.Fprintf(os.Stderr, "  journal: %s\n", r.PragmaJournal)
	}
	fmt.Fprintln(os.Stderr, "\nNext: `myserver up` (or `myserver app update` then deploy) — the new env var is picked up on next deploy.")
	// stdout: just the id, so scripts can capture it cleanly.
	fmt.Println(strconv.FormatInt(r.ID, 10))
	return nil
}

func runSqliteGet(args []string) error {
	fs := flag.NewFlagSet("sqlite get", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to your only team, or prompts)")
	id := fs.Int64("id", 0, "sqlite resource id (required)")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *id == 0 {
		return fmt.Errorf("--id is required (find via `myserver sqlite list`)")
	}

	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}
	r, err := api.getSqliteResource(*id)
	if err != nil {
		return fmt.Errorf("get sqlite %d: %w", *id, err)
	}
	fmt.Fprintf(os.Stderr, "SQLite resource #%d %q\n", r.ID, r.Name)
	fmt.Fprintf(os.Stderr, "  app:        %d\n", r.ApplicationID)
	fmt.Fprintf(os.Stderr, "  file:       %s\n", r.FilePath)
	fmt.Fprintf(os.Stderr, "  env_var:    %s\n", r.EnvVarKey)
	fmt.Fprintf(os.Stderr, "  connection: %s\n", r.ConnectionString)
	fmt.Fprintf(os.Stderr, "  journal:    %s\n", r.PragmaJournal)
	fmt.Fprintf(os.Stderr, "  status:     %s\n", r.Status)
	return nil
}

// runSqliteDelete handles `myserver sqlite delete`. Default keeps the
// backing volume so the user can re-attach a resource pointing at the
// same path and recover; --delete-volume wipes the .db file permanently.
//
// The --yes flag skips the typed-name confirmation for scripted use.
func runSqliteDelete(args []string) error {
	fs := flag.NewFlagSet("sqlite delete", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to your only team, or prompts)")
	id := fs.Int64("id", 0, "sqlite resource id (required)")
	deleteVolume := fs.Bool("delete-volume", false, "also wipe the backing volume (DESTROYS the .db file)")
	yes := fs.Bool("yes", false, "skip the confirmation prompt (DANGEROUS — scripted use only)")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *id == 0 {
		return fmt.Errorf("--id is required (find via `myserver sqlite list`)")
	}

	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}
	r, err := api.getSqliteResource(*id)
	if err != nil {
		return fmt.Errorf("fetch sqlite %d before delete: %w", *id, err)
	}

	if *deleteVolume {
		fmt.Fprintf(os.Stderr, "About to delete SQLite resource %q (id %d) AND its backing volume.\n", r.Name, r.ID)
		fmt.Fprintf(os.Stderr, "  file: %s — this is irreversible.\n", r.FilePath)
	} else {
		fmt.Fprintf(os.Stderr, "About to detach SQLite resource %q (id %d). Backing volume preserved.\n", r.Name, r.ID)
	}

	if !*yes {
		if !promptConfirm(fmt.Sprintf("Type %q to confirm: ", r.Name), r.Name) {
			return fmt.Errorf("aborted (input did not match resource name)")
		}
	}

	if err := api.deleteSqliteResource(*id, *deleteVolume); err != nil {
		return fmt.Errorf("delete sqlite %d: %w", *id, err)
	}
	if *deleteVolume {
		fmt.Fprintf(os.Stderr, "✓ Deleted SQLite resource %q (id %d) and its volume. Data is gone.\n", r.Name, r.ID)
	} else {
		fmt.Fprintf(os.Stderr, "✓ Detached SQLite resource %q (id %d). Recreate one pointing at %s to recover.\n", r.Name, r.ID, r.FilePath)
	}
	return nil
}
