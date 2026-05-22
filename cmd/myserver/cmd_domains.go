// myserver domains — manage an app's public hostnames.
//
// Thin wrapper over /applications/{id}/domains. Accepts either a JWT
// (default, from `myserver login`) OR a service token (via
// MYSERVER_APP_TOKEN env var with domains:write scope) — the API does
// the dispatch; the CLI doesn't have to care which auth mode applies.
//
// Use cases:
//   - "Add my custom domain to an app I deployed."
//   - "What domains is this app reachable at?" (debug, audit)
//   - "Take a domain off this app." (rare — usually app gets
//     replaced or fqdn rotated)
//
// The auto-injection flow makes this CLI subcommand also a sanity-
// check tool for app-token-authenticated containers: set
// MYSERVER_API_URL + MYSERVER_APP_TOKEN in the env, run
// `myserver domains list`, and confirm the token's domains:read
// scope works end-to-end.

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

func runDomains(args []string) error {
	if len(args) == 0 {
		domainsUsage()
		return fmt.Errorf("no subcommand specified")
	}
	switch args[0] {
	case "list", "ls":
		return runDomainsList(args[1:])
	case "add":
		return runDomainsAdd(args[1:])
	case "remove", "rm", "delete":
		return runDomainsRemove(args[1:])
	case "-h", "--help", "help":
		domainsUsage()
		return nil
	default:
		domainsUsage()
		return fmt.Errorf("unknown domains subcommand %q", args[0])
	}
}

func domainsUsage() {
	fmt.Fprintln(os.Stderr, "Usage: myserver domains <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  list     List hostnames the app is reachable at.")
	fmt.Fprintln(os.Stderr, "  add      Add a hostname (Caddy issues a cert via HTTP-01 / DNS-01).")
	fmt.Fprintln(os.Stderr, "  remove   Remove a hostname (refuses to remove the last one).")
}

func runDomainsList(args []string) error {
	fs := flag.NewFlagSet("domains list", flag.ContinueOnError)
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
	api, appResolved, err := resolveDomainsTarget(*teamID, *appID, *apiURL)
	if err != nil {
		return err
	}
	domains, err := api.listAppDomains(appResolved)
	if err != nil {
		return fmt.Errorf("list domains: %w", err)
	}
	if len(domains) == 0 {
		fmt.Fprintln(os.Stderr, "(no domains)")
		return nil
	}
	for _, d := range domains {
		fmt.Println(d)
	}
	return nil
}

func runDomainsAdd(args []string) error {
	fs := flag.NewFlagSet("domains add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to ./myserver.json or your only team)")
	appID := fs.Int64("app", 0, "application id (defaults to ./myserver.json)")
	hostname := fs.String("hostname", "", "hostname to add (required), e.g. example.com")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver domains add --hostname=<host> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Required:")
		fmt.Fprintln(os.Stderr, "  --hostname=<host>   e.g. example.com or *.tenant.example.com")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Inferred from ./myserver.json when omitted:")
		fmt.Fprintln(os.Stderr, "  --app=<id>          application id")
		fmt.Fprintln(os.Stderr, "  --team=<id>         team id")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Before adding a custom domain (not a *.sslip.io fallback):")
		fmt.Fprintln(os.Stderr, "  1. Point a CNAME from <hostname> at your app's existing FQDN")
		fmt.Fprintln(os.Stderr, "     (or A-record straight at the server IP).")
		fmt.Fprintln(os.Stderr, "  2. Wait until DNS resolves; THEN add. Caddy will fail HTTP-01")
		fmt.Fprintln(os.Stderr, "     cert issuance if DNS doesn't point at us yet.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Examples:")
		fmt.Fprintln(os.Stderr, "  myserver domains add --hostname=example.com")
		fmt.Fprintln(os.Stderr, "  myserver domains add --app=42 --hostname=*.preview.example.com")
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(*hostname) == "" {
		fs.Usage()
		return fmt.Errorf("--hostname is required")
	}
	api, appResolved, err := resolveDomainsTarget(*teamID, *appID, *apiURL)
	if err != nil {
		return err
	}
	domains, err := api.addAppDomain(appResolved, *hostname)
	if err != nil {
		return fmt.Errorf("add domain: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Added %s to app %d. Caddy is now serving:\n", *hostname, appResolved)
	for _, d := range domains {
		fmt.Fprintf(os.Stderr, "  - %s\n", d)
	}
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "First request to a new hostname triggers cert issuance.")
	fmt.Fprintln(os.Stderr, "Watch for cert errors in the Caddy logs if it doesn't resolve in ~30s.")
	return nil
}

func runDomainsRemove(args []string) error {
	fs := flag.NewFlagSet("domains remove", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to ./myserver.json or your only team)")
	appID := fs.Int64("app", 0, "application id (defaults to ./myserver.json)")
	hostname := fs.String("hostname", "", "hostname to remove (required)")
	yes := fs.Bool("yes", false, "skip confirmation prompt")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver domains remove --hostname=<host> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Refuses to remove the LAST domain — an app with no domains is")
		fmt.Fprintln(os.Stderr, "unreachable, which is a destructive change. Use `myserver app")
		fmt.Fprintln(os.Stderr, "update --fqdn=\"\"` if you really mean to take the app dark.")
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(*hostname) == "" {
		fs.Usage()
		return fmt.Errorf("--hostname is required")
	}
	api, appResolved, err := resolveDomainsTarget(*teamID, *appID, *apiURL)
	if err != nil {
		return err
	}
	if !*yes {
		if !promptConfirm(fmt.Sprintf("Remove %q from app %d? Type 'yes' to confirm: ", *hostname, appResolved), "yes") {
			return fmt.Errorf("aborted")
		}
	}
	if err := api.removeAppDomain(appResolved, *hostname); err != nil {
		return fmt.Errorf("remove domain: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Removed %s from app %d\n", *hostname, appResolved)
	return nil
}

// resolveDomainsTarget centralises the "find app id + team id + open
// the API client" boilerplate every subcommand shares. Falls back to
// myserver.json for the IDs when flags weren't passed.
func resolveDomainsTarget(teamID, appID int64, apiURL string) (*apiClient, int64, error) {
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
