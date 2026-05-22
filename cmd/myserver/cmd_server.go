// myserver server — register, list, and delete destination servers.
//
// The fast path: customer already has an SSH key for their VPS at
// ~/.ssh/id_rsa (or similar). One command uploads the key, registers
// the server, and validates SSH connectivity.
//
//   myserver server register \
//     --name=hetzner-prod \
//     --ip=1.2.3.4 \
//     --user=root \
//     --ssh-key=~/.ssh/id_rsa
//
// Under the hood this is TWO API calls (private-keys POST, then
// servers POST with the returned key id) plus an optional validate.
// We do the orchestration so the customer doesn't have to.

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func runServerCmd(args []string) error {
	if len(args) == 0 {
		serverUsage()
		return fmt.Errorf("no subcommand specified")
	}
	switch args[0] {
	case "list", "ls":
		return runServerList(args[1:])
	case "register", "add":
		return runServerRegister(args[1:])
	case "delete", "rm":
		return runServerDelete(args[1:])
	case "-h", "--help", "help":
		serverUsage()
		return nil
	default:
		serverUsage()
		return fmt.Errorf("unknown server subcommand %q", args[0])
	}
}

func serverUsage() {
	fmt.Fprintln(os.Stderr, "Usage: myserver server <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  list      List servers registered to the team.")
	fmt.Fprintln(os.Stderr, "  register  Register a new server. Uploads your SSH key + creates the server.")
	fmt.Fprintln(os.Stderr, "  delete    Soft-delete a server (apps + databases must be moved/removed first).")
}

func runServerList(args []string) error {
	fs := flag.NewFlagSet("server list", flag.ContinueOnError)
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
	servers, err := api.listServers()
	if err != nil {
		return fmt.Errorf("list servers: %w", err)
	}
	if len(servers) == 0 {
		fmt.Fprintln(os.Stderr, "(no servers)")
		return nil
	}
	for _, s := range servers {
		fmt.Printf("%d\t%s\t%s\t%s:%d\n", s.ID, s.Name, s.IP, s.User, s.Port)
	}
	return nil
}

// runServerRegister handles `myserver server register`. Reads the SSH
// private key from local disk, uploads it as a managed key, then
// registers the server with that key. Optionally validates the SSH
// connection (default: yes) so misconfigs fail at register time
// instead of much later during the first deploy.
//
// The key naming convention: same as the server name + "-key" suffix
// so operators can tell at a glance which key goes with which server.
// Multiple servers CAN share a key — pass --reuse-key=<id> in that
// case (skips the upload step entirely).
func runServerRegister(args []string) error {
	fs := flag.NewFlagSet("server register", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to your only team)")
	name := fs.String("name", "", "human label, e.g. 'hetzner-prod' (required)")
	ip := fs.String("ip", "", "server IP or hostname (required)")
	user := fs.String("user", "root", "SSH user")
	port := fs.Int("port", 22, "SSH port")
	sshKey := fs.String("ssh-key", "", "path to local SSH private key, e.g. ~/.ssh/id_rsa")
	reuseKey := fs.Int64("reuse-key", 0, "skip key upload + use existing private_key_id")
	description := fs.String("description", "", "human-readable description")
	noValidate := fs.Bool("no-validate", false, "skip SSH connectivity test after register")
	apiURL := fs.String("api", "", "myserver API URL")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver server register --name=<name> --ip=<ip> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Required:")
		fmt.Fprintln(os.Stderr, "  --name=<name>      human label, e.g. 'hetzner-prod'")
		fmt.Fprintln(os.Stderr, "  --ip=<ip>          server IP or hostname")
		fmt.Fprintln(os.Stderr, "  --ssh-key=<path>   path to local SSH private key (or use --reuse-key)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Optional:")
		fmt.Fprintln(os.Stderr, "  --user=<user>      SSH user (default: root)")
		fmt.Fprintln(os.Stderr, "  --port=<n>         SSH port (default: 22)")
		fmt.Fprintln(os.Stderr, "  --reuse-key=<id>   skip key upload, use existing key id (see Settings → Keys)")
		fmt.Fprintln(os.Stderr, "  --description=<s>  free-text notes")
		fmt.Fprintln(os.Stderr, "  --no-validate      don't run SSH connectivity test after register")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Examples:")
		fmt.Fprintln(os.Stderr, "  # New VPS, default user, default port:")
		fmt.Fprintln(os.Stderr, "  myserver server register --name=prod --ip=1.2.3.4 --ssh-key=~/.ssh/id_rsa")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  # Non-root user, custom port:")
		fmt.Fprintln(os.Stderr, "  myserver server register --name=staging --ip=10.0.0.5 \\")
		fmt.Fprintln(os.Stderr, "    --user=deploy --port=2222 --ssh-key=~/.ssh/staging_key")
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
	if strings.TrimSpace(*ip) == "" {
		fs.Usage()
		return fmt.Errorf("--ip is required")
	}
	if *sshKey == "" && *reuseKey == 0 {
		fs.Usage()
		return fmt.Errorf("either --ssh-key=<path> or --reuse-key=<id> is required")
	}

	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}

	keyID := *reuseKey
	if keyID == 0 {
		// Read + upload the local SSH key. We don't validate the PEM
		// shape here — let the server reject obviously-bad input so
		// future key formats (ed25519, FIDO2-backed, etc.) work
		// without CLI updates.
		path := expandHome(*sshKey)
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read SSH key %s: %w", path, err)
		}
		fmt.Fprintf(os.Stderr, "▸ uploading key from %s\n", path)
		key, err := api.createPrivateKey(CreatePrivateKeyRequest{
			Name:       *name + "-key",
			PrivateKey: string(raw),
		})
		if err != nil {
			return fmt.Errorf("upload private key: %w", err)
		}
		keyID = key.ID
		fmt.Fprintf(os.Stderr, "  ✓ key uploaded (id %d)\n", keyID)
	}

	req := CreateServerRequest{
		Name:         strings.TrimSpace(*name),
		IP:           strings.TrimSpace(*ip),
		User:         *user,
		Port:         *port,
		PrivateKeyID: keyID,
	}
	if s := strings.TrimSpace(*description); s != "" {
		req.Description = &s
	}

	fmt.Fprintf(os.Stderr, "▸ registering server %q (%s:%d as %s)\n", req.Name, req.IP, req.Port, req.User)
	srv, err := api.createServer(req)
	if err != nil {
		return fmt.Errorf("register server: %w", err)
	}
	fmt.Fprintf(os.Stderr, "  ✓ registered (id %d)\n", srv.ID)

	if !*noValidate {
		fmt.Fprintln(os.Stderr, "▸ validating SSH connectivity")
		if err := api.validateServer(srv.ID); err != nil {
			// Don't fail-out — the server IS registered, just couldn't
			// reach it. Common causes: firewall not yet open, DNS not
			// yet propagated, SSH key passphrase-protected (we don't
			// support those — would need to be stripped).
			fmt.Fprintf(os.Stderr, "  ⚠ validation failed: %v\n", err)
			fmt.Fprintln(os.Stderr, "    Server was registered but isn't reachable yet.")
			fmt.Fprintln(os.Stderr, "    Common causes: firewall, DNS, passphrase-protected key.")
			fmt.Fprintln(os.Stderr, "    Re-validate later via the web UI or `myserver` MCP tool.")
		} else {
			fmt.Fprintln(os.Stderr, "  ✓ SSH OK — server is ready to deploy to")
		}
	}

	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "Next: create an app with --server=%d to deploy here.\n", srv.ID)
	// stdout: the server id, so scripts can capture it
	fmt.Println(srv.ID)
	return nil
}

func runServerDelete(args []string) error {
	fs := flag.NewFlagSet("server delete", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id")
	id := fs.Int64("id", 0, "server id (required)")
	yes := fs.Bool("yes", false, "skip confirmation prompt")
	apiURL := fs.String("api", "", "myserver API URL")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver server delete --id=<id> [--yes]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Soft-deletes the server. Refuses if any apps or databases are still")
		fmt.Fprintln(os.Stderr, "  assigned to it — move or delete those resources first.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  The associated private key is NOT removed (it may be shared with")
		fmt.Fprintln(os.Stderr, "  other servers). To clear unused keys, use the web UI's key manager.")
	}
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
	// Look up the server name for the confirmation prompt.
	servers, err := api.listServers()
	if err != nil {
		return fmt.Errorf("list servers (for confirmation): %w", err)
	}
	var label string
	for _, s := range servers {
		if s.ID == *id {
			label = fmt.Sprintf("%s @ %s", s.Name, s.IP)
			break
		}
	}
	if label == "" {
		label = fmt.Sprintf("server-%d", *id)
	}

	fmt.Fprintf(os.Stderr, "About to delete %s (id %d).\n", label, *id)
	if !*yes {
		if !promptConfirm("\nType 'yes' to confirm: ", "yes") {
			return fmt.Errorf("aborted")
		}
	}
	if err := api.deleteServer(*id); err != nil {
		return fmt.Errorf("delete server: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Deleted %s (id %d)\n", label, *id)
	return nil
}

// expandHome resolves a leading "~/" in a path. Only does this for
// the simple case (~/foo) — doesn't handle ~user/foo because that
// requires looking up other users' home dirs and the CLI's only
// running as one user anyway.
func expandHome(path string) string {
	path = strings.TrimSpace(path)
	if !strings.HasPrefix(path, "~/") && path != "~" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path // fall back; let the os.ReadFile error speak
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}
