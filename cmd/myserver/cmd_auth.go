package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"syscall"

	"golang.org/x/term"
)

func runAuth(args []string) error {
	if len(args) == 0 {
		authUsage()
		return fmt.Errorf("no auth subcommand specified")
	}
	switch args[0] {
	case "register":
		return runAuthRegister(args[1:])
	case "-h", "--help", "help":
		authUsage()
		return nil
	default:
		return fmt.Errorf("unknown auth subcommand %q", args[0])
	}
}

func authUsage() {
	fmt.Fprintln(os.Stderr, "Usage: myserver auth <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  register  Register a new user and save credentials")
}

func runAuthRegister(args []string) error {
	fs := flag.NewFlagSet("auth register", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	apiURL := fs.String("api", "https://app.serverops.cloud", "myserver API URL")
	name := fs.String("name", "", "user display name")
	email := fs.String("email", "", "user email")
	password := fs.String("password", "", "user password (prompted when omitted)")
	timezone := fs.String("timezone", "", "IANA timezone identifier (optional)")
	noLogin := fs.Bool("no-login", false, "do not save returned credentials")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver auth register --email=<email> --name=<name> [--password=<password>] [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Registers a new user through /auth/register. By default the returned")
		fmt.Fprintln(os.Stderr, "  access token is saved just like `myserver login`.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	req := RegisterUserRequest{
		Name:     strings.TrimSpace(*name),
		Email:    strings.TrimSpace(*email),
		Password: *password,
		Timezone: strings.TrimSpace(*timezone),
	}
	if req.Name == "" {
		return fmt.Errorf("--name is required")
	}
	if req.Email == "" {
		return fmt.Errorf("--email is required")
	}
	if req.Password == "" {
		fmt.Fprint(os.Stderr, "password: ")
		pw, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		req.Password = string(pw)
	}
	if req.Password == "" {
		return fmt.Errorf("--password is required")
	}

	resp, err := apiRegister(*apiURL, req)
	if err != nil {
		return err
	}
	if !*noLogin {
		creds := &Credentials{APIURL: *apiURL, Token: resp.Tokens.AccessToken, Email: resp.User.Email}
		if err := saveCredentials(creds); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "✓ registered %s (token saved)\n", resp.User.Email)
		return nil
	}
	fmt.Fprintf(os.Stderr, "✓ registered %s\n", resp.User.Email)
	return nil
}
