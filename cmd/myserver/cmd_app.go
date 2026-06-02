// myserver app — application lifecycle subcommands.
//
// `app` is a router: it forwards to subcommand-specific runners. Today
// only `create` lives here; later additions (`app list`, `app delete`,
// `app rename`) slot in next to it without touching main.go.

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func runApp(args []string) error {
	if len(args) == 0 {
		appUsage()
		return fmt.Errorf("no subcommand specified")
	}
	switch args[0] {
	case "list", "ls":
		return runAppList(args[1:])
	case "get", "show":
		return runAppGet(args[1:])
	case "create":
		return runAppCreate(args[1:])
	case "update":
		return runAppUpdate(args[1:])
	case "start":
		return runAppLifecycle(args[1:], "start", "Start a stopped app on the given server")
	case "stop":
		return runAppLifecycle(args[1:], "stop", "Stop a running app on the given server")
	case "restart":
		return runAppLifecycle(args[1:], "restart", "Restart an app on the given server")
	case "deploy":
		return runAppDeploy(args[1:])
	case "delete", "rm":
		return runAppDelete(args[1:])
	case "-h", "--help", "help":
		appUsage()
		return nil
	default:
		appUsage()
		return fmt.Errorf("unknown app subcommand %q", args[0])
	}
}

func appUsage() {
	fmt.Fprintln(os.Stderr, "Usage: myserver app <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  list     List applications in the team.")
	fmt.Fprintln(os.Stderr, "  get      Show one application by id.")
	fmt.Fprintln(os.Stderr, "  create   Create a new application in a team's environment.")
	fmt.Fprintln(os.Stderr, "  update   Patch an existing application's config (build_pack, fqdn, image, …).")
	fmt.Fprintln(os.Stderr, "  start    Start a stopped application on the given server.")
	fmt.Fprintln(os.Stderr, "  stop     Stop a running application (containers gone, image+config remain).")
	fmt.Fprintln(os.Stderr, "  restart  Restart an application's containers on the given server.")
	fmt.Fprintln(os.Stderr, "  deploy   Deploy an application (pull/build image, (re)start its container).")
	fmt.Fprintln(os.Stderr, "  delete   Soft-delete the app. Cascade-removes deployments, env vars, tokens.")
}

// runAppList lists the applications in a team: one row per app with id,
// name, build pack, status, and FQDN. Mirrors `project list` / `server list`.
func runAppList(args []string) error {
	fs := flag.NewFlagSet("app list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to ./myserver.json or your only team)")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *teamID == 0 {
		if pc, err := loadProjectConfig(); err == nil && pc != nil {
			*teamID = pc.TeamID
		}
	}
	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}
	apps, err := api.listApps()
	if err != nil {
		return fmt.Errorf("list apps: %w", err)
	}
	if len(apps) == 0 {
		fmt.Fprintln(os.Stderr, "(no applications)")
		return nil
	}
	for _, a := range apps {
		fqdn := a.FQDN
		if fqdn == "" {
			fqdn = "-"
		}
		fmt.Printf("%d\t%s\t[%s]\t%s\t%s\n", a.ID, a.Name, a.BuildPack, a.Status, fqdn)
	}
	return nil
}

func runAppGet(args []string) error {
	fs := flag.NewFlagSet("app get", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to ./myserver.json or your only team)")
	appID := fs.Int64("app", 0, "application id (positional, --app, or ./myserver.json)")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver app get <id> [flags]")
	}
	var leadID int64
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if v, perr := strconv.ParseInt(args[0], 10, 64); perr == nil {
			leadID = v
			args = args[1:]
		}
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *appID == 0 {
		*appID = leadID
	}
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
		fs.Usage()
		return fmt.Errorf("app id is required (positional, --app, or myserver.json)")
	}
	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}
	app, err := api.getApp(*appID)
	if err != nil {
		return fmt.Errorf("get app %d: %w", *appID, err)
	}
	printApp(app)
	return nil
}

func printApp(app *Application) {
	fmt.Printf("id:\t%d\n", app.ID)
	fmt.Printf("name:\t%s\n", app.Name)
	fmt.Printf("build_pack:\t%s\n", app.BuildPack)
	fmt.Printf("status:\t%s\n", app.Status)
	fmt.Printf("environment_id:\t%d\n", app.EnvironmentID)
	if app.DestinationID != nil {
		fmt.Printf("server_id:\t%d\n", *app.DestinationID)
	}
	if app.FQDN != "" {
		fmt.Printf("fqdn:\t%s\n", app.FQDN)
	}
	if app.PortsExposes != "" {
		fmt.Printf("ports_exposes:\t%s\n", app.PortsExposes)
	}
	if app.DockerRegistryImageName != "" || app.DockerRegistryImageTag != "" {
		fmt.Printf("image:\t%s:%s\n", app.DockerRegistryImageName, app.DockerRegistryImageTag)
	}
	if app.DockerRegistryID != nil {
		fmt.Printf("docker_registry_id:\t%d\n", *app.DockerRegistryID)
	}
}

// runAppDeploy triggers a deployment of an existing application. The
// deployment fans out to the app's configured server(s) server-side, so
// there is no --server flag. Prints the deployment id to stdout for scripts.
func runAppDeploy(args []string) error {
	fs := flag.NewFlagSet("app deploy", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to ./myserver.json or your only team)")
	appID := fs.Int64("app", 0, "application id (positional, --app, or ./myserver.json)")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver app deploy <id> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Deploy an existing application: pull/build its image and (re)start")
		fmt.Fprintln(os.Stderr, "  its container on the app's server. The id may be given positionally,")
		fmt.Fprintln(os.Stderr, "  via --app, or inferred from ./myserver.json.")
	}

	var leadID int64
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if v, perr := strconv.ParseInt(args[0], 10, 64); perr == nil {
			leadID = v
			args = args[1:]
		}
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *appID == 0 {
		*appID = leadID
	}
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
		fs.Usage()
		return fmt.Errorf("app id is required (positional, --app, or myserver.json)")
	}

	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}
	dep, err := api.deployApp(*appID)
	if err != nil {
		return fmt.Errorf("deploy app %d: %w", *appID, err)
	}
	fmt.Fprintf(os.Stderr, "Deploy queued for app %d (deployment %d)\n", *appID, dep.ID)
	fmt.Println(dep.ID)
	return nil
}

// runAppLifecycle is the shared driver for start/stop/restart — they
// all hit /api/v1/applications/{id}/<action> with the same payload
// shape, only the URL changes. Factored so the three subcommands stay
// in sync (same flags, same prompts, same output formatting).
//
// Note: requires --server because an app may be deployed to MULTIPLE
// servers (multi-replica HA). Lifecycle ops target ONE server at a
// time so you can rolling-restart by hitting each in turn.
func runAppLifecycle(args []string, action, summary string) error {
	fs := flag.NewFlagSet("app "+action, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to ./myserver.json or your only team)")
	appID := fs.Int64("app", 0, "application id (defaults to ./myserver.json)")
	serverID := fs.Int64("server", 0, "server id (defaults to ./myserver.json)")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: myserver app %s [--app=<id>] [--server=<id>] [flags]\n", action)
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintf(os.Stderr, "  %s\n", summary)
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Inferred from ./myserver.json when omitted:")
		fmt.Fprintln(os.Stderr, "  --app=<id>     application id")
		fmt.Fprintln(os.Stderr, "  --server=<id>  destination server id")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "For multi-server apps, target each server explicitly to do a rolling op.")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	// Pull defaults from myserver.json the same way `myserver up` does.
	if *appID == 0 || *teamID == 0 || *serverID == 0 {
		if pc, err := loadProjectConfig(); err == nil && pc != nil {
			if *appID == 0 {
				*appID = pc.AppID
			}
			if *teamID == 0 {
				*teamID = pc.TeamID
			}
			if *serverID == 0 {
				*serverID = pc.ServerID
			}
		}
	}
	if *appID == 0 {
		return fmt.Errorf("--app is required (or run from a directory with myserver.json)")
	}
	if *serverID == 0 {
		return fmt.Errorf("--server is required (or set server_id in myserver.json)")
	}

	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}

	switch action {
	case "start":
		err = api.startApp(*appID, *serverID)
	case "stop":
		err = api.stopApp(*appID, *serverID)
	case "restart":
		err = api.restartApp(*appID, *serverID)
	default:
		return fmt.Errorf("unknown lifecycle action %q", action)
	}
	if err != nil {
		return fmt.Errorf("%s app %d on server %d: %w", action, *appID, *serverID, err)
	}
	// Capitalise action for the success line ("Start", "Stop", "Restart")
	// without pulling in golang.org/x/text/cases for a 4-letter word.
	verb := strings.ToUpper(action[:1]) + action[1:]
	fmt.Fprintf(os.Stderr, "✓ %s requested for app %d on server %d\n", verb, *appID, *serverID)
	fmt.Fprintln(os.Stderr, "  Container state will reflect the change within a few seconds.")
	return nil
}

// runAppDelete handles `myserver app delete`. Two-prompt safety: must
// type the app NAME to confirm, AND --yes to skip the prompt for
// scripted use requires explicit --app to prevent accidental deletion
// of the bound app.
func runAppDelete(args []string) error {
	fs := flag.NewFlagSet("app delete", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to ./myserver.json or your only team)")
	appID := fs.Int64("app", 0, "application id — REQUIRED, even from bound dir, to avoid accidents")
	yes := fs.Bool("yes", false, "skip confirmation prompt (scripted use only)")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver app delete --app=<id> [--yes]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Soft-deletes the application. Cascade-removes:")
		fmt.Fprintln(os.Stderr, "    - deployments + logs")
		fmt.Fprintln(os.Stderr, "    - env vars")
		fmt.Fprintln(os.Stderr, "    - SQLite resources")
		fmt.Fprintln(os.Stderr, "    - app service tokens")
		fmt.Fprintln(os.Stderr, "  Server-side containers are cleaned up async by the worker.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  --app is REQUIRED (not inferred from myserver.json) to prevent")
		fmt.Fprintln(os.Stderr, "  accidentally deleting the wrong app from a sibling project dir.")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *appID == 0 {
		fs.Usage()
		return fmt.Errorf("--app is required for delete (intentionally never inferred)")
	}

	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}
	app, err := api.getApp(*appID)
	if err != nil {
		return fmt.Errorf("look up app %d: %w", *appID, err)
	}

	fmt.Fprintf(os.Stderr, "About to delete app %q (id %d, build_pack=%s).\n", app.Name, app.ID, app.BuildPack)
	if app.FQDN != "" {
		fmt.Fprintf(os.Stderr, "  fqdn:  %s — will become unreachable\n", app.FQDN)
	}
	fmt.Fprintln(os.Stderr, "  This is irreversible.")

	if !*yes {
		if !promptConfirm(fmt.Sprintf("\nType %q to confirm: ", app.Name), app.Name) {
			return fmt.Errorf("aborted (input did not match app name)")
		}
	}
	if err := api.deleteApp(*appID); err != nil {
		return fmt.Errorf("delete app %d: %w", *appID, err)
	}
	fmt.Fprintf(os.Stderr, "✓ Deleted app %q (id %d)\n", app.Name, app.ID)
	return nil
}

// runAppCreate handles `myserver app create`. Three required inputs
// (--name, --env, --build-pack); everything else is optional and tied to
// the build pack.
func runAppCreate(args []string) error {
	fs := flag.NewFlagSet("app create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to your only team, or prompts)")
	envID := fs.Int64("env", 0, "environment id (required)")
	name := fs.String("name", "", "application name (required)")
	buildPack := fs.String("build-pack", "", "build pack: railpack|dockerfile|dockercompose|static|dockerimage (required)")
	description := fs.String("description", "", "human-readable description")
	gitRepo := fs.String("git", "", "git repository URL (e.g. https://github.com/you/repo)")
	gitBranch := fs.String("git-branch", "", "git branch (defaults to main)")
	fqdn := fs.String("fqdn", "", "public hostname (e.g. blog.example.com)")
	portsExposes := fs.String("ports-exposes", "", "container internal port (e.g. 3000)")
	serverID := fs.Int64("server", 0, "server id to deploy to (defaults to team's default)")
	dockerfile := fs.String("dockerfile-location", "", "path to Dockerfile inside the repo (build-pack=dockerfile)")
	composeLoc := fs.String("compose-location", "", "path to docker-compose.yml inside the repo (build-pack=dockercompose)")
	imageName := fs.String("image", "", "docker image name (build-pack=docker-image)")
	imageTag := fs.String("image-tag", "", "docker image tag (build-pack=docker-image, defaults to 'latest')")
	registryID := fs.Int64("registry-id", 0, "private Docker registry id for pulling --image")
	staticImage := fs.String("static-image", "", "static-site base image (build-pack=static)")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver app create --name=<name> --env=<id> --build-pack=<pack> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Required:")
		fmt.Fprintln(os.Stderr, "  --name=<name>         application name")
		fmt.Fprintln(os.Stderr, "  --env=<id>            environment id (find via `myserver` MCP list_environments or web UI)")
		fmt.Fprintln(os.Stderr, "  --build-pack=<pack>   railpack | dockerfile | dockercompose | static | dockerimage")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Common optional:")
		fmt.Fprintln(os.Stderr, "  --git=<url>           git repository URL (most build packs)")
		fmt.Fprintln(os.Stderr, "  --git-branch=<name>   branch to deploy from")
		fmt.Fprintln(os.Stderr, "  --fqdn=<host>         public hostname for routing")
		fmt.Fprintln(os.Stderr, "  --ports-exposes=<n>   container's internal listen port")
		fmt.Fprintln(os.Stderr, "  --server=<id>         pin to a specific server")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Build-pack specific:")
		fmt.Fprintln(os.Stderr, "  --dockerfile-location=<path>")
		fmt.Fprintln(os.Stderr, "  --compose-location=<path>")
		fmt.Fprintln(os.Stderr, "  --image=<name> --image-tag=<tag> [--registry-id=<id>]")
		fmt.Fprintln(os.Stderr, "  --static-image=<image>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Example:")
		fmt.Fprintln(os.Stderr, "  myserver app create --name=blog --env=3 --build-pack=railpack \\")
		fmt.Fprintln(os.Stderr, "    --git=https://github.com/you/blog --fqdn=blog.example.com --ports-exposes=3000")
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	// Required field validation up-front — fail fast with a helpful message.
	if strings.TrimSpace(*name) == "" {
		fs.Usage()
		return fmt.Errorf("--name is required")
	}
	if *envID == 0 {
		fs.Usage()
		return fmt.Errorf("--env is required (run `myserver init` to discover ids, or list via the MCP / web UI)")
	}
	if strings.TrimSpace(*buildPack) == "" {
		fs.Usage()
		return fmt.Errorf("--build-pack is required")
	}

	creds, err := loadCredentials()
	if err != nil {
		return fmt.Errorf("%w — run `myserver login` first", err)
	}
	if *apiURL != "" {
		creds.APIURL = *apiURL
	}

	// Team picker is optional: if there's exactly one team, skip prompting.
	if *teamID == 0 {
		api := newAPI(creds, 0)
		teams, err := api.listTeams()
		if err != nil {
			return fmt.Errorf("list teams: %w", err)
		}
		if len(teams) == 0 {
			return fmt.Errorf("you don't belong to any team yet")
		}
		picked, err := pickTeam(teams)
		if err != nil {
			return err
		}
		*teamID = picked
	}

	api := newAPI(creds, *teamID)

	req := CreateApplicationRequest{
		Name:          strings.TrimSpace(*name),
		EnvironmentID: *envID,
		BuildPack:     strings.TrimSpace(*buildPack),
	}
	if s := strings.TrimSpace(*description); s != "" {
		req.Description = &s
	}
	if s := strings.TrimSpace(*gitRepo); s != "" {
		req.GitRepository = &s
	}
	if s := strings.TrimSpace(*gitBranch); s != "" {
		req.GitBranch = &s
	}
	if s := strings.TrimSpace(*fqdn); s != "" {
		req.FQDN = &s
	}
	if s := strings.TrimSpace(*portsExposes); s != "" {
		req.PortsExposes = &s
	}
	if *serverID > 0 {
		req.ServerID = serverID
	}
	if s := strings.TrimSpace(*dockerfile); s != "" {
		req.DockerfileLocation = &s
	}
	if s := strings.TrimSpace(*composeLoc); s != "" {
		req.DockerComposeLocation = &s
	}
	if s := strings.TrimSpace(*imageName); s != "" {
		req.DockerRegistryImageName = &s
	}
	if s := strings.TrimSpace(*imageTag); s != "" {
		req.DockerRegistryImageTag = &s
	}
	if *registryID > 0 {
		req.DockerRegistryID = registryID
	}
	if s := strings.TrimSpace(*staticImage); s != "" {
		req.StaticImage = &s
	}

	app, err := api.createApp(req)
	if err != nil {
		return fmt.Errorf("create app: %w", err)
	}

	fmt.Fprintf(os.Stderr, "✓ Created app %q (id %d, build-pack %s)\n", app.Name, app.ID, app.BuildPack)
	if app.FQDN != "" {
		fmt.Fprintf(os.Stderr, "  fqdn:   %s\n", app.FQDN)
	}
	fmt.Fprintln(os.Stderr, "\nNext: `myserver init --app", app.ID, "` to bind the current directory, then `myserver up` to deploy.")
	return nil
}

// UpdateApplicationRequest mirrors the server's PATCH /applications/{id}
// surface, narrowed to the fields a CLI/AI customer actually wants to
// change inline. Keep this in sync with internal/handler/api/
// application_handler.go's `updateApplicationRequest`.
type UpdateApplicationRequest struct {
	Name                    *string `json:"name,omitempty"`
	Description             *string `json:"description,omitempty"`
	BuildPack               *string `json:"build_pack,omitempty"`
	GitRepository           *string `json:"git_repository,omitempty"`
	GitBranch               *string `json:"git_branch,omitempty"`
	FQDN                    *string `json:"fqdn,omitempty"`
	PortsExposes            *string `json:"ports_exposes,omitempty"`
	DockerfileLocation      *string `json:"dockerfile_location,omitempty"`
	DockerComposeLocation   *string `json:"docker_compose_location,omitempty"`
	DockerRegistryImageName *string `json:"docker_registry_image_name,omitempty"`
	DockerRegistryImageTag  *string `json:"docker_registry_image_tag,omitempty"`
	StaticImage             *string `json:"static_image,omitempty"`
	HealthCheckEnabled      *bool   `json:"health_check_enabled,omitempty"`
	HealthCheckPath         *string `json:"health_check_path,omitempty"`
}

// runAppUpdate handles `myserver app update`. PATCH the bound app (or
// any app id passed via --app) with whichever fields were specified.
// Critical use case: switching build_pack on an existing app, or
// clearing a stuck docker_registry_image_{name,tag} that's making the
// pipeline pull a stale image instead of building from source.
func runAppUpdate(args []string) error {
	fs := flag.NewFlagSet("app update", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	appID := fs.Int64("app", 0, "application id (defaults to ./myserver.json)")
	teamID := fs.Int64("team", 0, "team id (defaults to ./myserver.json or your only team)")
	name := fs.String("name", "", "rename the app")
	buildPack := fs.String("build-pack", "", "switch build pack: railpack|dockerfile|dockercompose|static|dockerimage")
	gitRepo := fs.String("git", "", "set git repository URL ('' to clear)")
	gitBranch := fs.String("git-branch", "", "set git branch ('' to clear)")
	fqdn := fs.String("fqdn", "", "set public hostname ('' to clear)")
	portsExposes := fs.String("ports-exposes", "", "set internal listen port (string)")
	dockerfileLoc := fs.String("dockerfile-location", "", "Dockerfile path inside repo")
	composeLoc := fs.String("compose-location", "", "docker-compose.yml path inside repo")
	imageName := fs.String("image-name", "", "docker image name (only for build_pack=dockerimage)")
	imageTag := fs.String("image-tag", "", "docker image tag")
	clearImage := fs.Bool("clear-image", false,
		"shorthand: clear both docker_registry_image_name and docker_registry_image_tag (use when switching FROM dockerimage TO another build pack — otherwise the pipeline keeps pulling the stale cached image)")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver app update [--app=<id>] <flags>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Patch an existing app. Only the flags you set are sent.")
		fmt.Fprintln(os.Stderr, "  Without --app, uses the app id from ./myserver.json.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Common recoveries:")
		fmt.Fprintln(os.Stderr, "  Switch from a stale dockerimage build:")
		fmt.Fprintln(os.Stderr, "    myserver app update --build-pack=dockerfile --clear-image")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Re-point a misrouted FQDN:")
		fmt.Fprintln(os.Stderr, "    myserver app update --fqdn=blog.example.com")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Hand off to an existing git repo:")
		fmt.Fprintln(os.Stderr, "    myserver app update --git=https://github.com/me/x --git-branch=main")
	}

	// flag.Visit only fires for flags actually passed on the CLI — we use
	// it below to distinguish "user passed empty string to CLEAR" from
	// "user didn't touch this field". empty default + Visit-set = clear.
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	creds, err := loadCredentials()
	if err != nil {
		return fmt.Errorf("%w — run `myserver login` first", err)
	}
	if *apiURL != "" {
		creds.APIURL = *apiURL
	}

	// Resolve appID + teamID from project config if missing.
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
	if *teamID == 0 {
		// Last-ditch: pick the only team so an admin can run `app update`
		// without `init` having been run first.
		api := newAPI(creds, 0)
		teams, terr := api.listTeams()
		if terr == nil && len(teams) == 1 {
			*teamID = teams[0].ID
		} else {
			return fmt.Errorf("--team is required when not in a project directory")
		}
	}

	// Walk the flagset to detect which flags were explicitly set. That's
	// how we distinguish "set to empty (clear)" from "leave alone".
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	var req UpdateApplicationRequest
	apply := func(flagName string, dst **string, value string) {
		if !set[flagName] {
			return
		}
		v := strings.TrimSpace(value)
		*dst = &v
	}
	apply("name", &req.Name, *name)
	apply("build-pack", &req.BuildPack, *buildPack)
	apply("git", &req.GitRepository, *gitRepo)
	apply("git-branch", &req.GitBranch, *gitBranch)
	apply("fqdn", &req.FQDN, *fqdn)
	apply("ports-exposes", &req.PortsExposes, *portsExposes)
	apply("dockerfile-location", &req.DockerfileLocation, *dockerfileLoc)
	apply("compose-location", &req.DockerComposeLocation, *composeLoc)
	apply("image-name", &req.DockerRegistryImageName, *imageName)
	apply("image-tag", &req.DockerRegistryImageTag, *imageTag)

	if *clearImage {
		empty := ""
		req.DockerRegistryImageName = &empty
		req.DockerRegistryImageTag = &empty
	}

	// Empty payload = nothing to do. Don't send a no-op PATCH.
	if isEmptyUpdate(&req) {
		return fmt.Errorf("nothing to update — pass at least one --<field>=<value> flag (see -h)")
	}

	api := newAPI(creds, *teamID)
	if err := api.updateApp(*appID, req); err != nil {
		return fmt.Errorf("update app %d: %w", *appID, err)
	}

	// Refetch so the printed summary reflects what the server actually
	// stored (e.g. trimmed values, server-side normalisation).
	app, err := api.getApp(*appID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✓ Updated app %d (refetch failed, but PATCH succeeded: %v)\n", *appID, err)
		return nil
	}
	fmt.Fprintf(os.Stderr, "✓ Updated app %q (id %d)\n", app.Name, app.ID)
	fmt.Fprintf(os.Stderr, "  build_pack: %s\n", app.BuildPack)
	if app.FQDN != "" {
		fmt.Fprintf(os.Stderr, "  fqdn:       %s\n", app.FQDN)
	}
	if app.DockerRegistryImageName != "" || app.DockerRegistryImageTag != "" {
		fmt.Fprintf(os.Stderr, "  image:      %s:%s\n", app.DockerRegistryImageName, app.DockerRegistryImageTag)
	} else {
		fmt.Fprintln(os.Stderr, "  image:      (cleared)")
	}
	return nil
}

// isEmptyUpdate reports true if no field on the request was set. Lets the
// CLI fail fast on a no-op invocation rather than wasting a round trip.
func isEmptyUpdate(r *UpdateApplicationRequest) bool {
	return r.Name == nil && r.Description == nil && r.BuildPack == nil &&
		r.GitRepository == nil && r.GitBranch == nil && r.FQDN == nil &&
		r.PortsExposes == nil && r.DockerfileLocation == nil &&
		r.DockerComposeLocation == nil && r.DockerRegistryImageName == nil &&
		r.DockerRegistryImageTag == nil && r.StaticImage == nil &&
		r.HealthCheckEnabled == nil && r.HealthCheckPath == nil
}
