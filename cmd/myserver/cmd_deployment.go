// myserver deployment — inspect deployment records.

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func runDeployment(args []string) error {
	if len(args) == 0 {
		deploymentUsage()
		return fmt.Errorf("no subcommand specified")
	}
	switch args[0] {
	case "get", "show":
		return runDeploymentGet(args[1:])
	case "-h", "--help", "help":
		deploymentUsage()
		return nil
	default:
		deploymentUsage()
		return fmt.Errorf("unknown deployment subcommand %q", args[0])
	}
}

func deploymentUsage() {
	fmt.Fprintln(os.Stderr, "Usage: myserver deployment <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  get  Show one deployment by id.")
}

func runDeploymentGet(args []string) error {
	fs := flag.NewFlagSet("deployment get", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	appID := fs.Int64("app", 0, "application id (defaults to ./myserver.json)")
	deployID := fs.Int64("deploy", 0, "deployment id (positional or --deploy)")
	teamID := fs.Int64("team", 0, "team id (defaults to ./myserver.json or your only team)")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver deployment get <deployment-id> --app=<app-id> [flags]")
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
	if *deployID == 0 {
		*deployID = leadID
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
		return fmt.Errorf("--app is required (or run from a directory with myserver.json)")
	}
	if *deployID == 0 {
		fs.Usage()
		return fmt.Errorf("deployment id is required")
	}
	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}
	dep, err := api.getDeployment(*appID, *deployID)
	if err != nil {
		return fmt.Errorf("get deployment %d: %w", *deployID, err)
	}
	printDeployment(dep)
	return nil
}

func printDeployment(dep *Deployment) {
	fmt.Printf("id:\t%d\n", dep.ID)
	fmt.Printf("application_id:\t%d\n", dep.ApplicationID)
	fmt.Printf("status:\t%s\n", dep.Status)
	if dep.DeploymentUUID != "" {
		fmt.Printf("uuid:\t%s\n", dep.DeploymentUUID)
	}
	if dep.CreatedAt != "" {
		fmt.Printf("created_at:\t%s\n", dep.CreatedAt)
	}
	if dep.Error != "" {
		fmt.Printf("error:\t%s\n", dep.Error)
	}
}
