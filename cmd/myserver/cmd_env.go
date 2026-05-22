// myserver env — environment lifecycle subcommands.
//
// Environments live under projects. Today we expose `list` (per-project
// or whole-team) and `create`. Mirrors `project` so the CLI feels
// uniform: pick a parent, name the child, done.
//
// Same motivation as `cmd_project.go` — the MCP toolset can list
// environments but cannot create them, so AI flows had to bounce out
// to the web UI to bootstrap a deploy target.

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func runEnv(args []string) error {
	if len(args) == 0 {
		envUsage()
		return fmt.Errorf("no subcommand specified")
	}
	switch args[0] {
	case "list", "ls":
		return runEnvList(args[1:])
	case "create":
		return runEnvCreate(args[1:])
	case "delete", "rm":
		return runEnvDelete(args[1:])
	case "-h", "--help", "help":
		envUsage()
		return nil
	default:
		envUsage()
		return fmt.Errorf("unknown env subcommand %q", args[0])
	}
}

func envUsage() {
	fmt.Fprintln(os.Stderr, "Usage: myserver env <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  list     List environments (under one project, or every project in the team).")
	fmt.Fprintln(os.Stderr, "  create   Create a new environment under a project.")
	fmt.Fprintln(os.Stderr, "  delete   Delete an environment (requires --force when it still holds resources).")
}

func runEnvList(args []string) error {
	fs := flag.NewFlagSet("env list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to your only team, or prompts)")
	projectID := fs.Int64("project", 0, "project id (default: list across every project in the team)")
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

	// Per-project: single round trip, simple output.
	if *projectID > 0 {
		envs, err := api.listEnvironments(*projectID)
		if err != nil {
			return fmt.Errorf("list environments: %w", err)
		}
		if len(envs) == 0 {
			fmt.Fprintln(os.Stderr, "(no environments)")
			return nil
		}
		for _, e := range envs {
			fmt.Printf("%d\t%s\n", e.ID, e.Name)
		}
		return nil
	}

	// Team-wide: walk every project so the user can see "what envs do I
	// have everywhere?" without having to look up project ids first.
	projects, err := api.listProjects()
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	if len(projects) == 0 {
		fmt.Fprintln(os.Stderr, "(no projects)")
		return nil
	}
	for _, p := range projects {
		envs, err := api.listEnvironments(p.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s (id %d): %v\n", p.Name, p.ID, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "%s (project %d):\n", p.Name, p.ID)
		if len(envs) == 0 {
			fmt.Fprintln(os.Stderr, "  (no environments)")
			continue
		}
		for _, e := range envs {
			fmt.Printf("%d\t%s\n", e.ID, e.Name)
		}
	}
	return nil
}

func runEnvCreate(args []string) error {
	fs := flag.NewFlagSet("env create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to your only team, or prompts)")
	projectID := fs.Int64("project", 0, "project id (required)")
	name := fs.String("name", "", "environment name (required, e.g. production / staging)")
	description := fs.String("description", "", "human-readable description")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver env create --project=<id> --name=<name> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Required:")
		fmt.Fprintln(os.Stderr, "  --project=<id>         project id (find via `myserver project list`)")
		fmt.Fprintln(os.Stderr, "  --name=<name>          environment name (e.g. production, staging, preview)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Optional:")
		fmt.Fprintln(os.Stderr, "  --description=<text>   human-readable description")
		fmt.Fprintln(os.Stderr, "  --team=<id>            team id (skip the team picker)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Example:")
		fmt.Fprintln(os.Stderr, "  myserver env create --project=3 --name=production")
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *projectID == 0 {
		fs.Usage()
		return fmt.Errorf("--project is required (run `myserver project list` to find one)")
	}
	if strings.TrimSpace(*name) == "" {
		fs.Usage()
		return fmt.Errorf("--name is required")
	}

	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}

	req := CreateEnvironmentRequest{Name: strings.TrimSpace(*name)}
	if s := strings.TrimSpace(*description); s != "" {
		req.Description = &s
	}
	e, err := api.createEnvironment(*projectID, req)
	if err != nil {
		return fmt.Errorf("create environment: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Created environment %q (id %d) in project %d\n", e.Name, e.ID, *projectID)
	fmt.Fprintf(os.Stderr, "\nNext: `myserver app create --env=%d --name=<app> --build-pack=<pack>`\n", e.ID)
	// Also print the id on stdout so scripts can capture it.
	fmt.Println(strconv.FormatInt(e.ID, 10))
	return nil
}

// runEnvDelete handles `myserver env delete`.
//
// Two layers of safety:
//   - We always fetch the deletion summary first so the user sees
//     exactly what the cascade will remove (apps, databases, services,
//     workspaces). No surprises.
//   - For non-empty envs, the server itself refuses without
//     `force=true`. We mirror that on the client by requiring `--force`
//     in addition to the prompt — `--yes` alone won't get past it.
//
// `--yes` skips the typed-name prompt for scripted use; `--force`
// adds `?force=true` to the request. Both must be passed for a
// non-interactive nuke of a populated environment.
func runEnvDelete(args []string) error {
	fs := flag.NewFlagSet("env delete", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to your only team, or prompts)")
	envID := fs.Int64("env", 0, "environment id (required)")
	yes := fs.Bool("yes", false, "skip the confirmation prompt (DANGEROUS — use only in scripts)")
	force := fs.Bool("force", false, "allow deleting an environment that still contains resources")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver env delete --env=<id> [--force] [--yes]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Required:")
		fmt.Fprintln(os.Stderr, "  --env=<id>      environment id (find via `myserver env list`)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Optional:")
		fmt.Fprintln(os.Stderr, "  --force         allow deleting an env that still holds resources")
		fmt.Fprintln(os.Stderr, "  --yes           skip confirmation (scripted use only)")
		fmt.Fprintln(os.Stderr, "  --team=<id>     team id (skip the team picker)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Confirmation: you'll be asked to type the environment name to proceed.")
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *envID == 0 {
		fs.Usage()
		return fmt.Errorf("--env is required")
	}

	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}

	summary, err := api.environmentDeletionSummary(*envID)
	if err != nil {
		return fmt.Errorf("fetch deletion summary: %w", err)
	}

	// We don't have a single-env GET in the api client, so derive the
	// human-readable name by walking projects → envs. Cheap relative
	// to the destructive op we're about to do.
	envName := envNameByID(api, *envID)
	if envName == "" {
		envName = fmt.Sprintf("env-%d", *envID)
	}

	fmt.Fprintf(os.Stderr, "About to delete environment %q (id %d).\n", envName, *envID)
	if summary.TotalResourceRefs > 0 {
		fmt.Fprintln(os.Stderr, "  This environment still contains:")
		printResourceLine(os.Stderr, "applications", summary.Applications, summary.ApplicationNames)
		printResourceLine(os.Stderr, "databases", summary.Databases, summary.DatabaseNames)
		printResourceLine(os.Stderr, "services", summary.Services, summary.ServiceNames)
		printResourceLine(os.Stderr, "workspaces", summary.Workspaces, summary.WorkspaceNames)
		if summary.ResourceLinks > 0 {
			fmt.Fprintf(os.Stderr, "    - %d resource link(s)\n", summary.ResourceLinks)
		}
		if !*force {
			return fmt.Errorf("environment is not empty — re-run with --force to confirm cascading delete")
		}
	} else {
		fmt.Fprintln(os.Stderr, "  Environment is empty.")
	}
	fmt.Fprintln(os.Stderr, "  This is irreversible.")

	if !*yes {
		prompt := fmt.Sprintf("\nType %q to confirm: ", envName)
		if !promptConfirm(prompt, envName) {
			return fmt.Errorf("aborted (input did not match environment name)")
		}
	}

	// Always pass force=true once we're past the confirmation. For
	// empty envs the flag is a no-op; for non-empty envs it's what the
	// server requires to skip its own RequiresConfirmationError.
	if err := api.deleteEnvironment(*envID, true); err != nil {
		return fmt.Errorf("delete environment: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Deleted environment %q (id %d)\n", envName, *envID)
	return nil
}

// envNameByID is a best-effort lookup for the human-readable name of
// an env. We don't fail the delete if it returns "" — the caller
// falls back to a generic label.
func envNameByID(api *apiClient, id int64) string {
	projects, err := api.listProjects()
	if err != nil {
		return ""
	}
	for _, p := range projects {
		envs, err := api.listEnvironments(p.ID)
		if err != nil {
			continue
		}
		for _, e := range envs {
			if e.ID == id {
				return e.Name
			}
		}
	}
	return ""
}

// printResourceLine renders one bullet of the deletion summary, with
// up to three example names so the user can recognise what they're
// about to nuke without us dumping potentially hundreds of items.
func printResourceLine(w *os.File, label string, count int, names []string) {
	if count == 0 {
		return
	}
	preview := ""
	if len(names) > 0 {
		const max = 3
		shown := names
		if len(shown) > max {
			shown = shown[:max]
		}
		preview = " (" + strings.Join(shown, ", ")
		if len(names) > max {
			preview += fmt.Sprintf(", +%d more", len(names)-max)
		}
		preview += ")"
	}
	fmt.Fprintf(w, "    - %d %s%s\n", count, label, preview)
}
