// myserver token — app service token subcommands.
//
// App service tokens (mst_…) let a DEPLOYED app call myserver's API
// on behalf of itself. Use case: a multi-tenant SaaS app deployed on
// myserver needs to add a custom domain for one of its tenants
// without prompting a human to log in.
//
// Plaintext-once policy: `token create` is the ONE moment the raw
// token leaves the server. The CLI prints it to stdout with a loud
// warning. Save it; rotation is "create new, revoke old."

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

func runToken(args []string) error {
	if len(args) == 0 {
		tokenUsage()
		return fmt.Errorf("no subcommand specified")
	}
	switch args[0] {
	case "create":
		return runTokenCreate(args[1:])
	case "list", "ls":
		return runTokenList(args[1:])
	case "revoke", "delete", "rm":
		return runTokenRevoke(args[1:])
	case "-h", "--help", "help":
		tokenUsage()
		return nil
	default:
		tokenUsage()
		return fmt.Errorf("unknown token subcommand %q", args[0])
	}
}

func tokenUsage() {
	fmt.Fprintln(os.Stderr, "Usage: myserver token <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  create   Mint a new app service token (mst_…). PRINTS THE PLAINTEXT ONCE.")
	fmt.Fprintln(os.Stderr, "  list     List tokens on an app (no plaintext).")
	fmt.Fprintln(os.Stderr, "  revoke   Soft-delete a token by id.")
}

// runTokenCreate handles `myserver token create`. Plaintext is printed
// to stdout (so it can be redirected to a file with `> token.txt`);
// human messaging goes to stderr.
func runTokenCreate(args []string) error {
	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to ./myserver.json or your only team)")
	appID := fs.Int64("app", 0, "application id (defaults to ./myserver.json)")
	name := fs.String("name", "", "human label for the token (required, e.g. 'app runtime')")
	scopes := fs.String("scopes", "", "comma-separated scopes (required). Allowed: domains:read,domains:write,env:read,deploy:trigger,app:read")
	autoInject := fs.Bool("auto-inject", false, "auto-inject MYSERVER_APP_TOKEN env var on every deploy of this app. At most one auto-inject token per app at a time.")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver token create --name=<name> --scopes=<comma-list> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Required:")
		fmt.Fprintln(os.Stderr, "  --name=<name>          human label (used in UI listings)")
		fmt.Fprintln(os.Stderr, "  --scopes=<csv>         e.g. 'domains:write,deploy:trigger'")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Inferred from ./myserver.json when omitted:")
		fmt.Fprintln(os.Stderr, "  --app=<id>             application id")
		fmt.Fprintln(os.Stderr, "  --team=<id>            team id")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Optional:")
		fmt.Fprintln(os.Stderr, "  --auto-inject          inject as MYSERVER_APP_TOKEN on every deploy")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Allowed scopes:")
		fmt.Fprintln(os.Stderr, "  domains:read     list app's hostnames")
		fmt.Fprintln(os.Stderr, "  domains:write    add / remove app's hostnames")
		fmt.Fprintln(os.Stderr, "  env:read         list env var keys (NOT values)")
		fmt.Fprintln(os.Stderr, "  deploy:trigger   enqueue a redeploy")
		fmt.Fprintln(os.Stderr, "  app:read         fetch the app's own metadata")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Examples:")
		fmt.Fprintln(os.Stderr, "  # bound dir, full-power runtime token, auto-injected")
		fmt.Fprintln(os.Stderr, "  myserver token create --name=runtime --scopes=domains:write,domains:read --auto-inject")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  # scripted, read-only token for a CI probe")
		fmt.Fprintln(os.Stderr, "  myserver token create --app=42 --name=ci-probe --scopes=app:read")
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
	if strings.TrimSpace(*scopes) == "" {
		fs.Usage()
		return fmt.Errorf("--scopes is required")
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

	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}

	scopeList := splitAndTrim(*scopes)
	if len(scopeList) == 0 {
		return fmt.Errorf("--scopes parsed to an empty list")
	}

	res, err := api.createAppToken(*appID, CreateAppTokenRequest{
		Name:       strings.TrimSpace(*name),
		Scopes:     scopeList,
		AutoInject: *autoInject,
	})
	if err != nil {
		return fmt.Errorf("create token: %w", err)
	}

	fmt.Fprintf(os.Stderr, "✓ Created token %q (id %d) on app %d\n", res.Token.Name, res.Token.ID, res.Token.ApplicationID)
	fmt.Fprintf(os.Stderr, "  prefix:      %s\n", res.Token.TokenPrefix)
	fmt.Fprintf(os.Stderr, "  scopes:      %s\n", strings.Join(res.Token.Scopes, ","))
	if res.Token.AutoInject {
		fmt.Fprintln(os.Stderr, "  auto_inject: yes — MYSERVER_APP_TOKEN will be injected on next deploy")
	}
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "⚠️  SAVE THIS TOKEN NOW. It will not be shown again.")
	fmt.Fprintln(os.Stderr, "")
	// stdout: the raw token, so a caller can do `myserver token create … > token.txt`.
	fmt.Println(res.Plaintext)
	return nil
}

func runTokenList(args []string) error {
	fs := flag.NewFlagSet("token list", flag.ContinueOnError)
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
	tokens, err := api.listAppTokens(*appID)
	if err != nil {
		return fmt.Errorf("list tokens: %w", err)
	}
	if len(tokens) == 0 {
		fmt.Fprintln(os.Stderr, "(no tokens)")
		return nil
	}
	for _, t := range tokens {
		state := "active"
		if t.RevokedAt != nil {
			state = "revoked"
		}
		inject := ""
		if t.AutoInject {
			inject = " auto-inject"
		}
		fmt.Printf("%d\t%s\t%s\t%s\t%s%s\n",
			t.ID, t.Name, t.TokenPrefix, strings.Join(t.Scopes, ","), state, inject)
	}
	return nil
}

func runTokenRevoke(args []string) error {
	fs := flag.NewFlagSet("token revoke", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to your only team, or prompts)")
	id := fs.Int64("id", 0, "token id (required, see `myserver token list`)")
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
		if !promptConfirm(fmt.Sprintf("Revoke token id %d? Type 'yes' to confirm: ", *id), "yes") {
			return fmt.Errorf("aborted")
		}
	}
	if err := api.revokeAppToken(*id); err != nil {
		return fmt.Errorf("revoke token %d: %w", *id, err)
	}
	fmt.Fprintf(os.Stderr, "✓ Revoked token %d\n", *id)
	return nil
}

// splitAndTrim splits a CSV string and trims each element. Empty
// elements are dropped (so "a,, b" becomes ["a","b"]).
func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
