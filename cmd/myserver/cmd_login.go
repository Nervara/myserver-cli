package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

// runLogin authenticates the CLI against a myserver instance.
//
// Default flow: email + password prompt (interactive TTY).
//
// --device flag: OAuth 2.0 Device Authorization Grant (RFC 8628). Prints a
// short user_code + verification URL, opens the browser, polls until the
// user clicks Approve. Use this when there's no TTY for password input
// (agent sandboxes, MCP servers, CI with browser access).
func runLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	apiURL := fs.String("api", "https://app.serverops.cloud", "myserver API URL")
	useDevice := fs.Bool("device", false, "use browser-based device login (no password prompt)")
	email := fs.String("email", "", "email (prompted if omitted)")
	noOpen := fs.Bool("no-open", false, "don't try to open the browser automatically (only with --device)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *useDevice {
		return loginWithDeviceCode(*apiURL, !*noOpen)
	}
	return loginWithPassword(*apiURL, *email)
}

// loginWithDeviceCode runs the browser-based OAuth device flow.
func loginWithDeviceCode(apiURL string, openBrowser bool) error {
	clientName := fmt.Sprintf("myserver-cli/%s %s/%s", cliVersion, runtime.GOOS, runtime.GOARCH)
	code, err := apiRequestDeviceCode(apiURL, clientName)
	if err != nil {
		return fmt.Errorf("request device code: %w", err)
	}

	fmt.Fprintf(os.Stderr, "\nTo finish signing in, visit:\n  %s\n\n", code.VerificationURIComplete)
	fmt.Fprintf(os.Stderr, "Code: %s\n", code.UserCode)
	fmt.Fprintln(os.Stderr, "Waiting for approval...")

	if openBrowser {
		_ = tryOpenBrowser(code.VerificationURIComplete)
	}

	interval := time.Duration(code.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(code.ExpiresIn) * time.Second)
	if code.ExpiresIn <= 0 {
		deadline = time.Now().Add(15 * time.Minute)
	}

	for {
		if time.Now().After(deadline) {
			return errors.New("login timed out — run `myserver login` again")
		}
		time.Sleep(interval)

		tokens, errCode, err := apiPollDeviceToken(apiURL, code.DeviceCode)
		if err != nil {
			return fmt.Errorf("poll: %w", err)
		}
		switch errCode {
		case "":
			// success
			user, err := fetchMe(apiURL, tokens.AccessToken)
			if err != nil {
				// Still save credentials — getMe is just for the friendly print.
				user = &User{Email: "(unknown)"}
			}
			creds := &Credentials{APIURL: apiURL, Token: tokens.AccessToken, Email: user.Email}
			if err := saveCredentials(creds); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "✓ logged in as %s (token saved)\n", user.Email)
			return nil
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 2 * time.Second
			continue
		case "access_denied":
			return errors.New("login was denied in the browser")
		case "expired_token":
			return errors.New("login code expired — run `myserver login` again")
		case "invalid_grant":
			return errors.New("invalid device code (was the CLI restarted?)")
		default:
			return fmt.Errorf("unexpected error: %s", errCode)
		}
	}
}

// loginWithPassword is the legacy email+password flow, kept behind --password.
func loginWithPassword(apiURL, email string) error {
	rd := bufio.NewReader(os.Stdin)
	if email == "" {
		fmt.Fprint(os.Stderr, "email: ")
		line, err := rd.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read email: %w", err)
		}
		email = strings.TrimSpace(line)
	}
	if email == "" {
		return fmt.Errorf("email is required")
	}
	fmt.Fprint(os.Stderr, "password: ")
	pw, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	if len(pw) == 0 {
		return fmt.Errorf("password is required")
	}
	resp, err := apiLogin(apiURL, email, string(pw))
	if err != nil {
		return err
	}
	creds := &Credentials{APIURL: apiURL, Token: resp.Tokens.AccessToken, Email: resp.User.Email}
	if err := saveCredentials(creds); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "✓ logged in as %s (token saved)\n", resp.User.Email)
	return nil
}

// tryOpenBrowser launches the platform default browser. Best-effort —
// failure is silent because the URL is also printed for manual paste.
func tryOpenBrowser(url string) error {
	var bin string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		bin = "open"
		args = []string{url}
	case "windows":
		bin = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		bin = "xdg-open"
		args = []string{url}
	}
	return exec.Command(bin, args...).Start()
}

// fetchMe is a one-shot /auth/me call used right after device-flow login to
// print the user's email in the success line.
func fetchMe(apiURL, token string) (*User, error) {
	c := newAPI(&Credentials{APIURL: apiURL, Token: token}, 0)
	return c.getMe()
}

// runWhoami prints the currently logged-in user. Hits /auth/me to confirm
// the saved token still works (so a stale credentials file shows as
// "logged out" instead of misleading the user with a cached email).
func runWhoami(_ []string) error {
	creds, err := loadCredentials()
	if err != nil {
		return fmt.Errorf("not logged in — run `myserver login`")
	}
	api := newAPI(creds, 0)
	user, err := api.getMe()
	if err != nil {
		return fmt.Errorf("token rejected by %s — run `myserver login` again (%w)", creds.APIURL, err)
	}
	fmt.Fprintf(os.Stderr, "✓ %s\n", user.Email)
	if user.Name != "" {
		fmt.Fprintf(os.Stderr, "  name:    %s\n", user.Name)
	}
	fmt.Fprintf(os.Stderr, "  user_id: %d\n", user.ID)
	if user.IsSystemAdmin {
		fmt.Fprintln(os.Stderr, "  role:    system admin")
	}
	fmt.Fprintf(os.Stderr, "  api:     %s\n", strings.TrimRight(creds.APIURL, "/"))
	return nil
}

func runLogout(_ []string) error {
	path, err := credentialsFile()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove credentials: %w", err)
	}
	fmt.Fprintln(os.Stderr, "✓ logged out")
	return nil
}
