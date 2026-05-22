package main

import (
	"flag"
	"fmt"
)

func runLogs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	deployID := fs.Int64("deploy", 0, "deployment id (default: latest)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	pc, err := loadProjectConfig()
	if err != nil {
		return err
	}
	api := newAPI(creds, pc.TeamID)

	id := *deployID
	if id == 0 {
		deps, err := api.listDeployments(pc.AppID, 1)
		if err != nil {
			return err
		}
		if len(deps) == 0 {
			return fmt.Errorf("no deployments yet for app %d", pc.AppID)
		}
		id = deps[0].ID
	}
	return tailDeployment(api, pc.AppID, id)
}

func runStatus(_ []string) error {
	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	pc, err := loadProjectConfig()
	if err != nil {
		return err
	}
	api := newAPI(creds, pc.TeamID)

	app, err := api.getApp(pc.AppID)
	if err != nil {
		return err
	}
	fmt.Printf("Account: %s @ %s\n", creds.Email, creds.APIURL)
	fmt.Printf("App:     %s (id %d) — status %s\n", app.Name, app.ID, app.Status)
	if app.FQDN != "" {
		fmt.Printf("FQDN:    %s\n", app.FQDN)
	}
	if app.DockerRegistryImageName != "" {
		fmt.Printf("Image:   %s:%s\n", app.DockerRegistryImageName, app.DockerRegistryImageTag)
	}
	deps, err := api.listDeployments(pc.AppID, 5)
	if err != nil {
		return err
	}
	fmt.Println("\nRecent deploys:")
	for _, d := range deps {
		fmt.Printf("  #%-4d %-10s %s\n", d.ID, d.Status, d.CreatedAt)
	}
	return nil
}
