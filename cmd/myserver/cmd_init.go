package main

// `myserver init` — interactive wiring of the current directory to an app.
//
// No flags needed in the typical case. The CLI lists the user's teams,
// asks them to pick one, lists apps in that team, asks them to pick one,
// and writes ./myserver.json. Flags exist for scripted scenarios:
//
//   myserver init                    interactive (the default)
//   myserver init --team 10          team picked, prompts only for app
//   myserver init --team 10 --app 17 fully scripted, no prompts
//
// When the team has no apps — or when the user picks "+ Create new app…"
// in the picker — `init` walks them through creating one inline. Build
// pack defaults are auto-detected from cwd files (Dockerfile → dockerfile,
// docker-compose.yml → dockercompose, package.json/etc. → railpack).

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	teamID := fs.Int64("team", 0, "team id (skip the team picker)")
	appID := fs.Int64("app", 0, "application id (skip the app picker)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	creds, err := loadCredentials()
	if err != nil {
		return fmt.Errorf("%w — run `myserver login` first", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Logged in as %s\n\n", creds.Email)

	// ── Pick the team ─────────────────────────────────────────────────
	if *teamID == 0 && creds.CurrentTeamID > 0 {
		*teamID = creds.CurrentTeamID
	}
	if *teamID == 0 {
		api := newAPI(creds, 0)
		teams, err := api.listTeams()
		if err != nil {
			return fmt.Errorf("list teams: %w", err)
		}
		if len(teams) == 0 {
			return fmt.Errorf("you don't belong to any team yet — create one in the UI first")
		}
		*teamID, err = pickTeam(teams)
		if err != nil {
			return err
		}
	}
	if err := rememberCurrentTeam(creds, *teamID); err != nil {
		return err
	}

	api := newAPI(creds, *teamID)

	// ── Pick or create the app ────────────────────────────────────────
	if *appID == 0 {
		apps, err := api.listApps()
		if err != nil {
			return fmt.Errorf("list apps: %w", err)
		}
		picked, err := pickAppOrCreate(api, apps, creds.APIURL)
		if err != nil {
			return err
		}
		*appID = picked
	}

	app, err := api.getApp(*appID)
	if err != nil {
		return fmt.Errorf("fetch app %d: %w", *appID, err)
	}

	// Resolve the destination server's host from the app's destination_id.
	serverID := int64(0)
	if app.DestinationID != nil {
		serverID = *app.DestinationID
	}
	serverHost := ""
	if serverID > 0 {
		servers, lerr := api.listServers()
		if lerr == nil {
			for _, s := range servers {
				if s.ID == serverID {
					serverHost = s.IP
					break
				}
			}
		}
	}

	pc := &ProjectConfig{
		TeamID:       *teamID,
		ServerID:     serverID,
		AppID:        app.ID,
		AppName:      app.Name,
		BuildPack:    app.BuildPack,
		PortsExposes: app.PortsExposes,
		FQDN:         app.FQDN,
	}
	if err := saveProjectConfig(pc); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\n✓ Wrote %s\n", projectConfigFn)
	fmt.Fprintf(os.Stderr, "  app:    %s (id %d)\n", pc.AppName, pc.AppID)
	if serverHost != "" {
		fmt.Fprintf(os.Stderr, "  server: %s\n", serverHost)
	}
	if pc.FQDN != "" {
		fmt.Fprintf(os.Stderr, "  url:    %s\n", pc.FQDN)
	}
	fmt.Fprintln(os.Stderr, "\nNext: `myserver up` to deploy.")
	return nil
}

// ── Pickers ──────────────────────────────────────────────────────────

func pickTeam(teams []Team) (int64, error) {
	if len(teams) == 1 {
		fmt.Fprintf(os.Stderr, "✓ Team: %s\n", teams[0].Name)
		return teams[0].ID, nil
	}
	fmt.Fprintln(os.Stderr, "Pick a team:")
	for i, t := range teams {
		fmt.Fprintf(os.Stderr, "  %d. %s\n", i+1, t.Name)
	}
	idx, err := promptChoice("Team [1-"+strconv.Itoa(len(teams))+"]: ", len(teams))
	if err != nil {
		return 0, err
	}
	picked := teams[idx]
	fmt.Fprintf(os.Stderr, "✓ Team: %s\n\n", picked.Name)
	return picked.ID, nil
}

// pickAppOrCreate shows the existing-apps picker with a leading "Create
// new app…" entry. Returns the chosen (or freshly-created) app ID.
//
// Selection logic: option 0 always means "create new". Options 1..N are
// the existing apps. When the team has zero apps we skip straight to the
// create flow — no point prompting with a one-option menu.
func pickAppOrCreate(api *apiClient, apps []Application, apiURL string) (int64, error) {
	if len(apps) == 0 {
		fmt.Fprintln(os.Stderr, "No apps in this team yet — let's create one.")
		return runAppCreateInteractive(api, apiURL)
	}

	fmt.Fprintln(os.Stderr, "Pick an app:")
	fmt.Fprintln(os.Stderr, "  0. + Create new app…")
	for i, a := range apps {
		hint := ""
		if a.Status != "" {
			hint = " (" + a.Status
			if a.FQDN != "" {
				hint += ", " + strings.TrimPrefix(strings.TrimPrefix(a.FQDN, "https://"), "http://")
			}
			hint += ")"
		} else if a.FQDN != "" {
			hint = " (" + strings.TrimPrefix(strings.TrimPrefix(a.FQDN, "https://"), "http://") + ")"
		}
		fmt.Fprintf(os.Stderr, "  %d. %s%s\n", i+1, a.Name, hint)
	}
	idx, err := promptChoiceWithZero("App [0-"+strconv.Itoa(len(apps))+"]: ", len(apps))
	if err != nil {
		return 0, err
	}
	if idx == -1 {
		return runAppCreateInteractive(api, apiURL)
	}
	picked := apps[idx]
	fmt.Fprintf(os.Stderr, "✓ App: %s\n", picked.Name)
	return picked.ID, nil
}

// runAppCreateInteractive walks the user through inline app creation:
// pick project → pick environment → confirm name + build-pack (defaults
// from cwd file detection) → POST /applications/.
//
// Returns the new app's ID. Failure modes are surfaced with actionable
// messages (no projects → tell them where to create one) so the user
// isn't dumped back to a bare error.
func runAppCreateInteractive(api *apiClient, apiURL string) (int64, error) {
	_ = apiURL // kept in signature for callers; inline-create flow no longer needs it.

	projects, err := api.listProjects()
	if err != nil {
		return 0, fmt.Errorf("list projects: %w", err)
	}
	var project *Project
	if len(projects) == 0 {
		fmt.Fprintln(os.Stderr, "No projects in this team yet — let's create one.")
		project, err = createProjectInteractive(api)
		if err != nil {
			return 0, err
		}
	} else {
		project, err = pickProjectOrCreate(api, projects)
		if err != nil {
			return 0, err
		}
	}

	envs, err := api.listEnvironments(project.ID)
	if err != nil {
		return 0, fmt.Errorf("list environments for project %d: %w", project.ID, err)
	}
	var env *Environment
	if len(envs) == 0 {
		fmt.Fprintf(os.Stderr, "Project %q has no environments yet — let's create one.\n", project.Name)
		env, err = createEnvironmentInteractive(api, project.ID)
		if err != nil {
			return 0, err
		}
	} else {
		env, err = pickEnvironmentOrCreate(api, project.ID, envs)
		if err != nil {
			return 0, err
		}
	}

	cwd, _ := os.Getwd()
	defaultName := sanitizeAppName(filepath.Base(cwd))
	if defaultName == "" {
		defaultName = "app"
	}
	defaultBuildPack := detectBuildPack(cwd)

	rd := bufio.NewReader(os.Stdin)

	name, err := promptLine(rd, fmt.Sprintf("App name [%s]: ", defaultName), defaultName)
	if err != nil {
		return 0, err
	}
	name = sanitizeAppName(name)
	if name == "" {
		return 0, fmt.Errorf("app name is required")
	}

	buildPack, err := promptBuildPack(rd, defaultBuildPack)
	if err != nil {
		return 0, err
	}

	fmt.Fprintf(os.Stderr, "\n▸ creating app %q in %s/%s (build_pack=%s)\n", name, project.Name, env.Name, buildPack)
	app, err := api.createApp(CreateApplicationRequest{
		Name:          name,
		EnvironmentID: env.ID,
		BuildPack:     buildPack,
	})
	if err != nil {
		return 0, fmt.Errorf("create app: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Created app %q (id %d)\n", app.Name, app.ID)
	return app.ID, nil
}

// pickProjectOrCreate is the project-picker flavour of pickAppOrCreate:
// option 0 is "+ Create new project…", 1..N are the existing projects.
// Used during inline app creation so the customer never has to leave
// the CLI to bootstrap their workspace.
func pickProjectOrCreate(api *apiClient, projects []Project) (*Project, error) {
	fmt.Fprintln(os.Stderr, "Pick a project:")
	fmt.Fprintln(os.Stderr, "  0. + Create new project…")
	for i, p := range projects {
		fmt.Fprintf(os.Stderr, "  %d. %s\n", i+1, p.Name)
	}
	idx, err := promptChoiceWithZero("Project [0-"+strconv.Itoa(len(projects))+"]: ", len(projects))
	if err != nil {
		return nil, err
	}
	if idx == -1 {
		return createProjectInteractive(api)
	}
	picked := projects[idx]
	fmt.Fprintf(os.Stderr, "✓ Project: %s\n", picked.Name)
	return &picked, nil
}

// pickEnvironmentOrCreate is the environment-picker flavour of
// pickAppOrCreate: option 0 is "+ Create new environment…", 1..N are
// the existing envs in the parent project.
func pickEnvironmentOrCreate(api *apiClient, projectID int64, envs []Environment) (*Environment, error) {
	fmt.Fprintln(os.Stderr, "Pick an environment:")
	fmt.Fprintln(os.Stderr, "  0. + Create new environment…")
	for i, e := range envs {
		fmt.Fprintf(os.Stderr, "  %d. %s\n", i+1, e.Name)
	}
	idx, err := promptChoiceWithZero("Environment [0-"+strconv.Itoa(len(envs))+"]: ", len(envs))
	if err != nil {
		return nil, err
	}
	if idx == -1 {
		return createEnvironmentInteractive(api, projectID)
	}
	picked := envs[idx]
	fmt.Fprintf(os.Stderr, "✓ Environment: %s\n", picked.Name)
	return &picked, nil
}

// createProjectInteractive prompts for a project name and POSTs it.
// Description is intentionally not prompted — keeps the inline flow
// to a single question, and the user can edit the description in the
// UI later if they care.
func createProjectInteractive(api *apiClient) (*Project, error) {
	rd := bufio.NewReader(os.Stdin)
	name, err := promptLine(rd, "Project name: ", "")
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("project name is required")
	}
	p, err := api.createProject(CreateProjectRequest{Name: name})
	if err != nil {
		return nil, fmt.Errorf("create project: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Created project %q (id %d)\n", p.Name, p.ID)
	return p, nil
}

// createEnvironmentInteractive prompts for an environment name (default
// "production" since that's by far the most common first env) and
// POSTs it under the given project.
func createEnvironmentInteractive(api *apiClient, projectID int64) (*Environment, error) {
	rd := bufio.NewReader(os.Stdin)
	name, err := promptLine(rd, "Environment name [production]: ", "production")
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("environment name is required")
	}
	e, err := api.createEnvironment(projectID, CreateEnvironmentRequest{Name: name})
	if err != nil {
		return nil, fmt.Errorf("create environment: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Created environment %q (id %d)\n", e.Name, e.ID)
	return e, nil
}

// detectBuildPack returns the most likely build_pack for cwd, based on
// signal files. The detection prioritises explicit Docker setups
// (Dockerfile / docker-compose) over language manifests because if the
// customer hand-wrote one of those, that's clearly what they want.
//
// Falls back to railpack — railpack itself does deeper auto-detection
// (Node, Python, Go, Rust, Java, Ruby, …) so we don't try to enumerate
// every language here.
func detectBuildPack(dir string) string {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	if has("docker-compose.yml") || has("docker-compose.yaml") ||
		has("compose.yml") || has("compose.yaml") {
		return "dockercompose"
	}
	if has("Dockerfile") {
		return "dockerfile"
	}
	// Static-site detection: index.html present and NO language manifests
	// that would otherwise route to railpack. This catches plain
	// docs / landing pages / built SPAs without false-positives on Node
	// projects that happen to ship an index.html.
	if has("index.html") &&
		!has("package.json") && !has("go.mod") &&
		!has("pyproject.toml") && !has("requirements.txt") &&
		!has("Cargo.toml") && !has("Gemfile") {
		return "static"
	}
	return "railpack"
}

// sanitizeAppName produces a server-acceptable name from a freeform input.
// Server validates `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`; we lower-case +
// replace runs of non-conforming chars with '-' so a directory named
// "My Cool App!" yields "my-cool-app".
func sanitizeAppName(in string) string {
	in = strings.TrimSpace(in)
	if in == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(in))
	dash := false
	for _, r := range strings.ToLower(in) {
		switch {
		case r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '.' || r == '_' || r == '-':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-._")
	if len(out) > 63 {
		out = strings.Trim(out[:63], "-._")
	}
	return out
}

// promptBuildPack confirms the auto-detected build pack or lets the user
// pick a different one. Single Enter accepts the default — that's the
// magic-feeling case for the 95% of customers whose project structure
// is unambiguous.
func promptBuildPack(rd *bufio.Reader, def string) (string, error) {
	options := []string{"railpack", "dockerfile", "dockercompose", "static", "dockerimage"}
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Build pack options:")
	for i, o := range options {
		marker := "  "
		if o == def {
			marker = "→ "
		}
		fmt.Fprintf(os.Stderr, "%s%d. %s\n", marker, i+1, o)
	}
	prompt := fmt.Sprintf("Build pack [1-%d, default %s]: ", len(options), def)
	for attempt := 0; attempt < 2; attempt++ {
		fmt.Fprint(os.Stderr, prompt)
		line, err := rd.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("read input: %w", err)
		}
		s := strings.TrimSpace(line)
		if s == "" {
			return def, nil
		}
		// Accept either a number or the literal name — both unambiguous.
		for _, o := range options {
			if s == o {
				return o, nil
			}
		}
		v, err := strconv.Atoi(s)
		if err == nil && v >= 1 && v <= len(options) {
			return options[v-1], nil
		}
		fmt.Fprintf(os.Stderr, "  please enter 1-%d or one of: %s\n", len(options), strings.Join(options, ", "))
	}
	return "", fmt.Errorf("invalid input twice — aborting")
}

// promptLine reads one line, returning the default if the user just hits
// Enter. Used for the app-name prompt where the cwd basename is almost
// always what the user wants.
func promptLine(rd *bufio.Reader, prompt, def string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	line, err := rd.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read input: %w", err)
	}
	s := strings.TrimSpace(line)
	if s == "" {
		return def, nil
	}
	return s, nil
}

// promptChoice reads a 1-based integer, returns the 0-based index. Re-prompts
// once on bad input — keeps the friction low without the user having to
// re-run the whole command.
func promptChoice(prompt string, n int) (int, error) {
	rd := bufio.NewReader(os.Stdin)
	for attempt := 0; attempt < 2; attempt++ {
		fmt.Fprint(os.Stderr, prompt)
		line, err := rd.ReadString('\n')
		if err != nil {
			return 0, fmt.Errorf("read input: %w", err)
		}
		v, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || v < 1 || v > n {
			fmt.Fprintf(os.Stderr, "  please enter a number between 1 and %d.\n", n)
			continue
		}
		return v - 1, nil
	}
	return 0, fmt.Errorf("invalid input twice — aborting")
}

// promptConfirm asks a yes/no question on stderr/stdin. Returns true
// only on an explicit "y" or "yes" (case-insensitive). Empty input
// (just Enter) and anything else default to false — destructive
// commands should never proceed on an ambiguous answer. The expected
// answer is also matched against requireExact when set, so callers
// like "type the project name to confirm" can re-use this prompt.
func promptConfirm(prompt, requireExact string) bool {
	rd := bufio.NewReader(os.Stdin)
	fmt.Fprint(os.Stderr, prompt)
	line, err := rd.ReadString('\n')
	if err != nil {
		return false
	}
	s := strings.TrimSpace(line)
	if requireExact != "" {
		return s == requireExact
	}
	switch strings.ToLower(s) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// promptChoiceWithZero behaves like promptChoice but accepts 0 as the
// "create new" sentinel. Returns -1 on 0, otherwise a 0-based index into
// a list of length n. Lets `init`'s app picker have a leading
// "+ Create new app…" entry without losing the existing 1-based UX.
func promptChoiceWithZero(prompt string, n int) (int, error) {
	rd := bufio.NewReader(os.Stdin)
	for attempt := 0; attempt < 2; attempt++ {
		fmt.Fprint(os.Stderr, prompt)
		line, err := rd.ReadString('\n')
		if err != nil {
			return 0, fmt.Errorf("read input: %w", err)
		}
		v, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || v < 0 || v > n {
			fmt.Fprintf(os.Stderr, "  please enter 0 (create new) or a number between 1 and %d.\n", n)
			continue
		}
		if v == 0 {
			return -1, nil
		}
		return v - 1, nil
	}
	return 0, fmt.Errorf("invalid input twice — aborting")
}
