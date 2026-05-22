// myserver db — managed-database (postgres/mysql/redis/etc.) management.
//
// First-class managed databases are distinct from SQLite resources
// (which attach to a single app's volume). Managed databases run as
// their own containers, get their own credentials, and can be linked
// to multiple apps. See `myserver sqlite` for the single-app variant.

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

func runDB(args []string) error {
	if len(args) == 0 {
		dbUsage()
		return fmt.Errorf("no subcommand specified")
	}
	switch args[0] {
	case "list", "ls":
		return runDBList(args[1:])
	case "get", "show":
		return runDBGet(args[1:])
	case "start":
		return runDBLifecycle(args[1:], "start")
	case "stop":
		return runDBLifecycle(args[1:], "stop")
	case "restart":
		return runDBLifecycle(args[1:], "restart")
	case "delete", "rm":
		return runDBDelete(args[1:])
	case "-h", "--help", "help":
		dbUsage()
		return nil
	default:
		dbUsage()
		return fmt.Errorf("unknown db subcommand %q", args[0])
	}
}

func dbUsage() {
	fmt.Fprintln(os.Stderr, "Usage: myserver db <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  list     List managed databases in the team.")
	fmt.Fprintln(os.Stderr, "  get      Show one database by id.")
	fmt.Fprintln(os.Stderr, "  start    Start a stopped database container.")
	fmt.Fprintln(os.Stderr, "  stop     Stop a running database container (volume preserved).")
	fmt.Fprintln(os.Stderr, "  restart  Restart the container (same image, same volume).")
	fmt.Fprintln(os.Stderr, "  delete   Soft-delete the database resource. Volume preserved by default.")
}

func runDBList(args []string) error {
	fs := flag.NewFlagSet("db list", flag.ContinueOnError)
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
	dbs, err := api.listDatabases()
	if err != nil {
		return fmt.Errorf("list databases: %w", err)
	}
	if len(dbs) == 0 {
		fmt.Fprintln(os.Stderr, "(no databases)")
		return nil
	}
	for _, d := range dbs {
		pub := ""
		if d.IsPublic && d.PublicPort != nil {
			pub = fmt.Sprintf(" public=:%d", *d.PublicPort)
		}
		fmt.Printf("%d\t%s\t%s\t%s\tenv=%d\tstatus=%s%s\n",
			d.ID, d.Name, d.DatabaseType, d.Image, d.EnvironmentID, d.Status, pub)
	}
	return nil
}

func runDBGet(args []string) error {
	fs := flag.NewFlagSet("db get", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to your only team, or prompts)")
	id := fs.Int64("id", 0, "database id (required)")
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
	d, err := api.getDatabase(*id)
	if err != nil {
		return fmt.Errorf("get database %d: %w", *id, err)
	}
	if d == nil {
		return fmt.Errorf("database %d not found", *id)
	}
	fmt.Fprintf(os.Stderr, "Database #%d %q\n", d.ID, d.Name)
	fmt.Fprintf(os.Stderr, "  type:    %s\n", d.DatabaseType)
	fmt.Fprintf(os.Stderr, "  image:   %s\n", d.Image)
	fmt.Fprintf(os.Stderr, "  env:     %d\n", d.EnvironmentID)
	fmt.Fprintf(os.Stderr, "  status:  %s\n", d.Status)
	if d.IsPublic && d.PublicPort != nil {
		fmt.Fprintf(os.Stderr, "  public:  :%d\n", *d.PublicPort)
	}
	return nil
}

// runDBLifecycle shares the start/stop/restart pattern with cmd_app.go
// (intentionally — same shape, same flags) but for databases the
// "server" concept isn't part of the lifecycle call (the database's
// destination is implicit in the resource).
func runDBLifecycle(args []string, action string) error {
	fs := flag.NewFlagSet("db "+action, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to your only team)")
	id := fs.Int64("id", 0, "database id (required, see `myserver db list`)")
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
	switch action {
	case "start":
		err = api.startDatabase(*id)
	case "stop":
		err = api.stopDatabase(*id)
	case "restart":
		err = api.restartDatabase(*id)
	default:
		return fmt.Errorf("unknown lifecycle action %q", action)
	}
	if err != nil {
		return fmt.Errorf("%s database %d: %w", action, *id, err)
	}
	verb := strings.ToUpper(action[:1]) + action[1:]
	fmt.Fprintf(os.Stderr, "✓ %s requested for database %d\n", verb, *id)
	return nil
}

func runDBDelete(args []string) error {
	fs := flag.NewFlagSet("db delete", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id")
	id := fs.Int64("id", 0, "database id (required)")
	yes := fs.Bool("yes", false, "skip confirmation prompt")
	apiURL := fs.String("api", "", "myserver API URL")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver db delete --id=<id> [--yes]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Soft-deletes the managed database. The underlying Docker volume is")
		fmt.Fprintln(os.Stderr, "  preserved by default (recreating a DB with the same name recovers data).")
		fmt.Fprintln(os.Stderr, "  To permanently destroy the volume, use the web UI or hit the API directly")
		fmt.Fprintln(os.Stderr, "  with ?delete_volume=true.")
	}
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
	d, err := api.getDatabase(*id)
	if err != nil {
		return fmt.Errorf("look up database %d: %w", *id, err)
	}
	if d == nil {
		return fmt.Errorf("database %d not found", *id)
	}
	fmt.Fprintf(os.Stderr, "About to delete database %q (id %d, type %s).\n", d.Name, d.ID, d.DatabaseType)
	fmt.Fprintln(os.Stderr, "  Volume preserved. Recreate with the same name to recover data.")
	if !*yes {
		if !promptConfirm(fmt.Sprintf("\nType %q to confirm: ", d.Name), d.Name) {
			return fmt.Errorf("aborted")
		}
	}
	if err := api.deleteDatabase(*id); err != nil {
		return fmt.Errorf("delete database %d: %w", *id, err)
	}
	fmt.Fprintf(os.Stderr, "✓ Deleted database %q (id %d). Volume preserved.\n", d.Name, d.ID)
	return nil
}
