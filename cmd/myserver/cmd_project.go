// myserver project — project lifecycle subcommands.
//
// `project` is a router. Today we expose `list` and `create`; later
// additions (`project delete`, `project rename`) slot in next to them
// without touching main.go.
//
// Why this exists: the MCP tools can list projects but cannot create
// them, so AI customers had to fall back to the web UI to bootstrap
// a workspace. Surfacing creation in the CLI closes that gap.

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func runProject(args []string) error {
	if len(args) == 0 {
		projectUsage()
		return fmt.Errorf("no subcommand specified")
	}
	switch args[0] {
	case "list", "ls":
		return runProjectList(args[1:])
	case "create":
		return runProjectCreate(args[1:])
	case "delete", "rm":
		return runProjectDelete(args[1:])
	case "-h", "--help", "help":
		projectUsage()
		return nil
	default:
		projectUsage()
		return fmt.Errorf("unknown project subcommand %q", args[0])
	}
}

func projectUsage() {
	fmt.Fprintln(os.Stderr, "Usage: myserver project <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  list     List projects in the current team.")
	fmt.Fprintln(os.Stderr, "  create   Create a new project in a team.")
	fmt.Fprintln(os.Stderr, "  delete   Delete a project (cascades to its environments).")
}

func runProjectList(args []string) error {
	fs := flag.NewFlagSet("project list", flag.ContinueOnError)
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
	projects, err := api.listProjects()
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	if len(projects) == 0 {
		fmt.Fprintln(os.Stderr, "(no projects)")
		return nil
	}
	for _, p := range projects {
		desc := ""
		if p.Description != "" {
			desc = " — " + p.Description
		}
		fmt.Printf("%d\t%s%s\n", p.ID, p.Name, desc)
	}
	return nil
}

func runProjectCreate(args []string) error {
	fs := flag.NewFlagSet("project create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to your only team, or prompts)")
	name := fs.String("name", "", "project name (required)")
	description := fs.String("description", "", "human-readable description")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver project create --name=<name> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Required:")
		fmt.Fprintln(os.Stderr, "  --name=<name>          project name")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Optional:")
		fmt.Fprintln(os.Stderr, "  --description=<text>   human-readable description")
		fmt.Fprintln(os.Stderr, "  --team=<id>            team id (skip the team picker)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Example:")
		fmt.Fprintln(os.Stderr, "  myserver project create --name=acme --description='Acme prod stack'")
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

	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}

	req := CreateProjectRequest{Name: strings.TrimSpace(*name)}
	if s := strings.TrimSpace(*description); s != "" {
		req.Description = &s
	}
	p, err := api.createProject(req)
	if err != nil {
		return fmt.Errorf("create project: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Created project %q (id %d)\n", p.Name, p.ID)
	fmt.Fprintf(os.Stderr, "\nNext: `myserver env create --project=%d --name=production` to add an environment.\n", p.ID)
	// Also print the id on stdout so scripts can capture it.
	fmt.Println(strconv.FormatInt(p.ID, 10))
	return nil
}

// runProjectDelete handles `myserver project delete`. Confirmation
// is mandatory by default — the user has to type the project name to
// proceed, mirroring how GitHub / Vercel gate destructive
// repo/project deletes. `--yes` skips the prompt for scripted use.
//
// We list the project's environments first so the warning lands with
// real numbers ("this will also remove 3 environments and everything
// in them") instead of a vague "are you sure?".
func runProjectDelete(args []string) error {
	fs := flag.NewFlagSet("project delete", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to your only team, or prompts)")
	projectID := fs.Int64("project", 0, "project id (required)")
	yes := fs.Bool("yes", false, "skip the confirmation prompt (DANGEROUS — use only in scripts)")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver project delete --project=<id> [--yes]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Required:")
		fmt.Fprintln(os.Stderr, "  --project=<id>  project id (find via `myserver project list`)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Optional:")
		fmt.Fprintln(os.Stderr, "  --yes           skip confirmation (scripted use only)")
		fmt.Fprintln(os.Stderr, "  --team=<id>     team id (skip the team picker)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Confirmation: you'll be asked to type the project name to proceed.")
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *projectID == 0 {
		fs.Usage()
		return fmt.Errorf("--project is required")
	}

	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}

	// Look the project up so we can refer to it by name and verify
	// it exists before we even start scaring the user with prompts.
	projects, err := api.listProjects()
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	var project *Project
	for i := range projects {
		if projects[i].ID == *projectID {
			project = &projects[i]
			break
		}
	}
	if project == nil {
		return fmt.Errorf("project %d not found in this team", *projectID)
	}

	envs, err := api.listEnvironments(project.ID)
	if err != nil {
		return fmt.Errorf("list environments for project %d: %w", project.ID, err)
	}

	fmt.Fprintf(os.Stderr, "About to delete project %q (id %d).\n", project.Name, project.ID)
	if len(envs) > 0 {
		fmt.Fprintf(os.Stderr, "  This will also delete %d environment(s) and everything inside them:\n", len(envs))
		for _, e := range envs {
			fmt.Fprintf(os.Stderr, "    - %s (id %d)\n", e.Name, e.ID)
		}
	}
	fmt.Fprintln(os.Stderr, "  This is irreversible.")

	if !*yes {
		prompt := fmt.Sprintf("\nType %q to confirm: ", project.Name)
		if !promptConfirm(prompt, project.Name) {
			return fmt.Errorf("aborted (input did not match project name)")
		}
	}

	if err := api.deleteProject(project.ID); err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Deleted project %q (id %d)\n", project.Name, project.ID)
	return nil
}

// resolveTeamAPI loads creds and returns an apiClient bound to the
// caller-supplied team — or, if no team was passed, the user's only
// team (auto-pick) or the result of an interactive picker. Shared by
// `project` and `env` subcommands so they have identical UX.
func resolveTeamAPI(teamID int64, apiURL string) (*apiClient, int64, error) {
	creds, err := loadCredentials()
	if err != nil {
		return nil, 0, fmt.Errorf("%w — run `myserver login` first", err)
	}
	if apiURL != "" {
		creds.APIURL = apiURL
	}
	if teamID == 0 {
		api := newAPI(creds, 0)
		teams, err := api.listTeams()
		if err != nil {
			return nil, 0, fmt.Errorf("list teams: %w", err)
		}
		if len(teams) == 0 {
			return nil, 0, fmt.Errorf("you don't belong to any team yet")
		}
		picked, err := pickTeam(teams)
		if err != nil {
			return nil, 0, err
		}
		teamID = picked
	}
	return newAPI(creds, teamID), teamID, nil
}
