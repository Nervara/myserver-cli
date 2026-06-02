// myserver registry — manage private Docker registry credentials.

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

func runRegistry(args []string) error {
	if len(args) == 0 {
		registryUsage()
		return fmt.Errorf("no subcommand specified")
	}
	switch args[0] {
	case "list", "ls":
		return runRegistryList(args[1:])
	case "get", "show":
		return runRegistryGet(args[1:])
	case "create", "add":
		return runRegistryCreate(args[1:])
	case "update":
		return runRegistryUpdate(args[1:])
	case "delete", "rm":
		return runRegistryDelete(args[1:])
	case "-h", "--help", "help":
		registryUsage()
		return nil
	default:
		registryUsage()
		return fmt.Errorf("unknown registry subcommand %q", args[0])
	}
}

func registryUsage() {
	fmt.Fprintln(os.Stderr, "Usage: myserver registry <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  list    List private Docker registries for the team.")
	fmt.Fprintln(os.Stderr, "  get     Show one registry credential (password redacted).")
	fmt.Fprintln(os.Stderr, "  create  Add a private Docker registry credential.")
	fmt.Fprintln(os.Stderr, "  update  Patch registry metadata or credentials.")
	fmt.Fprintln(os.Stderr, "  delete  Delete a registry credential (refuses if in use).")
}

func runRegistryList(args []string) error {
	fs := flag.NewFlagSet("registry list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to your only team)")
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
	regs, err := api.listDockerRegistries()
	if err != nil {
		return fmt.Errorf("list registries: %w", err)
	}
	if len(regs) == 0 {
		fmt.Fprintln(os.Stderr, "(no registries)")
		return nil
	}
	for _, r := range regs {
		system := ""
		if r.IsSystem {
			system = " system"
		}
		fmt.Printf("%d\t%s\t%s\t%s%s\n", r.ID, r.Name, r.RegistryURL, r.Username, system)
	}
	return nil
}

func runRegistryGet(args []string) error {
	fs := flag.NewFlagSet("registry get", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	id := fs.Int64("id", 0, "registry id (positional or --id)")
	teamID := fs.Int64("team", 0, "team id (defaults to your only team)")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")
	leadID := parseLeadingInt64(&args)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *id == 0 {
		*id = leadID
	}
	if *id == 0 {
		return fmt.Errorf("registry id is required")
	}
	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}
	reg, err := api.getDockerRegistry(*id)
	if err != nil {
		return fmt.Errorf("get registry %d: %w", *id, err)
	}
	printRegistry(reg)
	return nil
}

func runRegistryCreate(args []string) error {
	fs := flag.NewFlagSet("registry create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to your only team)")
	name := fs.String("name", "", "human label for the registry (required)")
	registryURL := fs.String("url", "", "registry host, e.g. example.azurecr.io (required)")
	username := fs.String("username", "", "registry username (required)")
	password := fs.String("password", "", "registry password or token (prefer --password-stdin)")
	passwordStdin := fs.Bool("password-stdin", false, "read registry password/token from stdin")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver registry create --name=<name> --url=<host> --username=<user> [--password-stdin]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Example:")
		fmt.Fprintln(os.Stderr, "  az acr credential show --name prod --query passwords[0].value -o tsv | \\")
		fmt.Fprintln(os.Stderr, "    myserver registry create --name=acr-prod --url=prod.azurecr.io --username=<user> --password-stdin")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	pass, err := resolveRegistryPassword(*password, *passwordStdin)
	if err != nil {
		return err
	}
	req := CreateDockerRegistryRequest{
		Name:        strings.TrimSpace(*name),
		RegistryURL: strings.TrimSpace(*registryURL),
		Username:    strings.TrimSpace(*username),
		Password:    pass,
	}
	if err := validateCreateRegistryRequest(req); err != nil {
		fs.Usage()
		return err
	}
	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}
	reg, err := api.createDockerRegistry(req)
	if err != nil {
		return fmt.Errorf("create registry: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Created registry %q (id %d)\n", reg.Name, reg.ID)
	fmt.Println(reg.ID)
	return nil
}

func runRegistryUpdate(args []string) error {
	fs := flag.NewFlagSet("registry update", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	id := fs.Int64("id", 0, "registry id (positional or --id)")
	teamID := fs.Int64("team", 0, "team id (defaults to your only team)")
	name := fs.String("name", "", "new registry label")
	registryURL := fs.String("url", "", "new registry host")
	username := fs.String("username", "", "new registry username")
	password := fs.String("password", "", "new registry password or token (prefer --password-stdin)")
	passwordStdin := fs.Bool("password-stdin", false, "read new password/token from stdin")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")
	leadID := parseLeadingInt64(&args)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *id == 0 {
		*id = leadID
	}
	if *id == 0 {
		return fmt.Errorf("registry id is required")
	}
	req, err := buildUpdateRegistryRequest(*name, *registryURL, *username, *password, *passwordStdin)
	if err != nil {
		return err
	}
	if emptyUpdateRegistryRequest(req) {
		return fmt.Errorf("nothing to update")
	}
	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}
	reg, err := api.updateDockerRegistry(*id, req)
	if err != nil {
		return fmt.Errorf("update registry %d: %w", *id, err)
	}
	fmt.Fprintf(os.Stderr, "Updated registry %q (id %d)\n", reg.Name, reg.ID)
	printRegistry(reg)
	return nil
}

func runRegistryDelete(args []string) error {
	fs := flag.NewFlagSet("registry delete", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	id := fs.Int64("id", 0, "registry id (positional or --id)")
	teamID := fs.Int64("team", 0, "team id (defaults to your only team)")
	apiURL := fs.String("api", "", "myserver API URL (defaults to logged-in URL)")
	leadID := parseLeadingInt64(&args)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *id == 0 {
		*id = leadID
	}
	if *id == 0 {
		return fmt.Errorf("registry id is required")
	}
	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}
	if err := api.deleteDockerRegistry(*id); err != nil {
		return fmt.Errorf("delete registry %d: %w", *id, err)
	}
	fmt.Fprintf(os.Stderr, "Deleted registry %d\n", *id)
	return nil
}

func parseLeadingInt64(args *[]string) int64 {
	if len(*args) == 0 || strings.HasPrefix((*args)[0], "-") {
		return 0
	}
	v, err := strconv.ParseInt((*args)[0], 10, 64)
	if err != nil {
		return 0
	}
	*args = (*args)[1:]
	return v
}

func resolveRegistryPassword(password string, fromStdin bool) (string, error) {
	if !fromStdin {
		return strings.TrimSpace(password), nil
	}
	if strings.TrimSpace(password) != "" {
		return "", fmt.Errorf("--password and --password-stdin are mutually exclusive")
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read password from stdin: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func validateCreateRegistryRequest(req CreateDockerRegistryRequest) error {
	if req.Name == "" {
		return fmt.Errorf("--name is required")
	}
	if req.RegistryURL == "" {
		return fmt.Errorf("--url is required")
	}
	if req.Username == "" {
		return fmt.Errorf("--username is required")
	}
	if req.Password == "" {
		return fmt.Errorf("--password or --password-stdin is required")
	}
	return nil
}

func buildUpdateRegistryRequest(name, url, username, password string, passwordStdin bool) (UpdateDockerRegistryRequest, error) {
	var req UpdateDockerRegistryRequest
	if s := strings.TrimSpace(name); s != "" {
		req.Name = &s
	}
	if s := strings.TrimSpace(url); s != "" {
		req.RegistryURL = &s
	}
	if s := strings.TrimSpace(username); s != "" {
		req.Username = &s
	}
	pass, err := resolveRegistryPassword(password, passwordStdin)
	if err != nil {
		return req, err
	}
	if pass != "" {
		req.Password = &pass
	}
	return req, nil
}

func emptyUpdateRegistryRequest(req UpdateDockerRegistryRequest) bool {
	return req.Name == nil && req.RegistryURL == nil && req.Username == nil && req.Password == nil
}

func printRegistry(reg *DockerRegistry) {
	fmt.Printf("id:\t%d\n", reg.ID)
	fmt.Printf("name:\t%s\n", reg.Name)
	fmt.Printf("url:\t%s\n", reg.RegistryURL)
	fmt.Printf("user:\t%s\n", reg.Username)
	if reg.IsSystem {
		fmt.Println("system:\ttrue")
	}
}
