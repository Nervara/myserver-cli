// myserver tenant — whole-tenant customer backup & restore.
//
// Unlike `myserver backup` (the platform/system-admin dump of the ENTIRE
// control plane), this operates on YOUR space only: every project in your
// team — environments, apps, databases, services, env vars (including secret
// values) and DB passwords — captured into one encrypted file. By default it
// restores on this instance or another instance sharing the same APP_SECRET;
// pass a recovery key to make the backup portable across unrelated instances.
//
//	myserver tenant export                 # → ./tenant-team<N>-<ts>.mbak
//	myserver tenant restore --file=<f>     # recreate everything in your team
//
// Restore is additive: resources matched by name are skipped, so re-running a
// restore never duplicates or overwrites.

package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type tenantExportResponse struct {
	Filename   string `json:"filename"`
	Projects   int    `json:"projects"`
	Artifacts  int    `json:"artifacts"`
	ExportedAt string `json:"exported_at"`
	Backup     string `json:"backup"`
}

const tenantRecoveryKeyHeader = "X-Tenant-Backup-Recovery-Key"

func (a *apiClient) exportTenant(withData bool, s3StorageID int64, recoveryKey string) (*tenantExportResponse, error) {
	path := "/api/v1/tenant/export"
	if withData {
		path += fmt.Sprintf("?include_data=true&s3_storage_id=%d", s3StorageID)
	}
	headers := map[string]string(nil)
	if strings.TrimSpace(recoveryKey) != "" {
		headers = map[string]string{tenantRecoveryKeyHeader: strings.TrimSpace(recoveryKey)}
	}
	var out tenantExportResponse
	if err := a.doWithHeaders("GET", path, nil, headers, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (a *apiClient) importTenant(blob string, dryRun bool, remap map[string]int64, targetServerID *int64, recoveryKey string) (map[string]any, error) {
	body := map[string]any{"backup": blob, "dry_run": dryRun}
	if targetServerID != nil {
		body["target_server_id"] = *targetServerID
	} else if len(remap) > 0 {
		body["server_remap"] = remap
	}
	if strings.TrimSpace(recoveryKey) != "" {
		body["recovery_key"] = strings.TrimSpace(recoveryKey)
	}
	var out map[string]any
	if err := a.do("POST", "/api/v1/tenant/import", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func runTenant(args []string) error {
	if len(args) == 0 {
		tenantUsage()
		return fmt.Errorf("no subcommand specified")
	}
	switch args[0] {
	case "export", "backup":
		return runTenantExport(args[1:])
	case "import", "restore":
		return runTenantImport(args[1:])
	case "-h", "--help", "help":
		tenantUsage()
		return nil
	default:
		tenantUsage()
		return fmt.Errorf("unknown tenant subcommand %q", args[0])
	}
}

func tenantUsage() {
	fmt.Fprintln(os.Stderr, "Usage: myserver tenant <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  export    Back up your ENTIRE tenant (all projects, config + secrets) to a file.")
	fmt.Fprintln(os.Stderr, "            Add --with-data --s3-storage-id=N to ALSO store data artifacts in S3.")
	fmt.Fprintln(os.Stderr, "  restore   Recreate a tenant from a backup file into your team (loads DB data if present).")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Without --with-data: config + secrets only (env vars, DB passwords, FQDNs).")
	fmt.Fprintln(os.Stderr, "With --with-data: bundle references S3-backed database/volume artifacts.")
	fmt.Fprintln(os.Stderr, "For portable SaaS→standalone restore, use --generate-recovery-key-file on export")
	fmt.Fprintln(os.Stderr, "and --recovery-key-file on restore.")
}

func runTenantExport(args []string) error {
	fs := flag.NewFlagSet("tenant export", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to your only team)")
	apiURL := fs.String("api", "", "myserver API URL")
	output := fs.String("output", "", "file to write the backup to (default: ./<auto-name>.mbak)")
	withData := fs.Bool("with-data", false, "ALSO store database/volume data artifacts in S3")
	s3StorageID := fs.Int64("s3-storage-id", 0, "S3 storage ID for --with-data artifacts")
	recoveryKeyFile := fs.String("recovery-key-file", "", "read a tenant backup recovery key from this file")
	generateRecoveryKeyFile := fs.String("generate-recovery-key-file", "", "generate a recovery key, write it to this file, and encrypt the backup with it")
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
	if *withData && *s3StorageID <= 0 {
		return fmt.Errorf("--s3-storage-id is required with --with-data")
	}
	if *recoveryKeyFile != "" && *generateRecoveryKeyFile != "" {
		return fmt.Errorf("use either --recovery-key-file or --generate-recovery-key-file, not both")
	}
	recoveryKey := ""
	if *recoveryKeyFile != "" {
		recoveryKey, err = readRecoveryKeyFile(*recoveryKeyFile)
		if err != nil {
			return err
		}
	}
	if *generateRecoveryKeyFile != "" {
		recoveryKey, err = generateTenantRecoveryKey()
		if err != nil {
			return err
		}
		if err := writeRecoveryKeyFile(*generateRecoveryKeyFile, recoveryKey); err != nil {
			return err
		}
	}

	if *withData {
		fmt.Fprintf(os.Stderr, "▸ Exporting tenant WITH DATA (all projects, secrets + S3 artifacts; storage_id=%d)...\n", *s3StorageID)
	} else {
		fmt.Fprintln(os.Stderr, "▸ Exporting tenant (all projects, config + secrets; use --with-data for DB data)...")
	}
	resp, err := api.exportTenant(*withData, *s3StorageID, recoveryKey)
	if err != nil {
		return fmt.Errorf("export tenant: %w", err)
	}

	outPath := *output
	if outPath == "" {
		outPath = "./" + resp.Filename
	}
	if err := os.WriteFile(outPath, []byte(resp.Backup), 0o600); err != nil {
		return fmt.Errorf("write backup file %s: %w", outPath, err)
	}
	fmt.Fprintf(os.Stderr, "✓ Backed up %d project(s), %d data artifact(s) → %s\n", resp.Projects, resp.Artifacts, outPath)
	if *generateRecoveryKeyFile != "" {
		fmt.Fprintf(os.Stderr, "  Recovery key written to %s. Store it separately from the backup.\n", *generateRecoveryKeyFile)
	}
	if recoveryKey != "" {
		fmt.Fprintln(os.Stderr, "  Encrypted with a recovery key. Restore with:")
		fmt.Fprintf(os.Stderr, "    myserver tenant restore --file=%s --recovery-key-file=<key-file>\n", outPath)
	} else {
		fmt.Fprintln(os.Stderr, "  Encrypted with this instance's APP_SECRET. Restore with:")
		fmt.Fprintf(os.Stderr, "    myserver tenant restore --file=%s\n", outPath)
	}
	// stdout: the path, for scripting
	fmt.Println(outPath)
	return nil
}

func runTenantImport(args []string) error {
	fs := flag.NewFlagSet("tenant restore", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id to restore INTO (defaults to your only team)")
	apiURL := fs.String("api", "", "myserver API URL")
	file := fs.String("file", "", "backup file produced by `tenant export` (required)")
	dryRun := fs.Bool("dry-run", false, "report what would be created without creating anything")
	serverRemap := fs.String("server-remap", "", "remap source→target server IDs, e.g. 34:8,35:9 (advanced)")
	targetServerID := fs.Int64("target-server-id", 0, "put all server-scoped resources onto this server (simple path, mutually exclusive with --server-remap)")
	recoveryKeyFile := fs.String("recovery-key-file", "", "read the tenant backup recovery key from this file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *file == "" {
		return fmt.Errorf("--file is required (the backup produced by `tenant export`)")
	}

	blob, err := os.ReadFile(*file)
	if err != nil {
		return fmt.Errorf("read backup file %s: %w", *file, err)
	}

	if *targetServerID != 0 && *serverRemap != "" {
		return fmt.Errorf("--target-server-id and --server-remap are mutually exclusive")
	}
	remap, err := parseServerRemap(*serverRemap)
	if err != nil {
		return err
	}
	var tgtID *int64
	if *targetServerID != 0 {
		tgtID = targetServerID
	}
	recoveryKey := ""
	if *recoveryKeyFile != "" {
		recoveryKey, err = readRecoveryKeyFile(*recoveryKeyFile)
		if err != nil {
			return err
		}
	}

	api, _, err := resolveTeamAPI(*teamID, *apiURL)
	if err != nil {
		return err
	}

	if *dryRun {
		fmt.Fprintln(os.Stderr, "▸ Dry run — restoring nothing, just reporting...")
	} else {
		fmt.Fprintln(os.Stderr, "▸ Restoring tenant into your team (additive; existing names skipped)...")
	}
	result, err := api.importTenant(strings.TrimSpace(string(blob)), *dryRun, remap, tgtID, recoveryKey)
	if err != nil {
		return fmt.Errorf("restore tenant: %w", err)
	}

	pretty, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(pretty))
	if !*dryRun {
		fmt.Fprintln(os.Stderr, "✓ Restore complete. Deploy the restored apps from the dashboard or `myserver up`.")
	}
	return nil
}

func generateTenantRecoveryKey() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate recovery key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func readRecoveryKeyFile(path string) (string, error) {
	data, err := os.ReadFile(expandHome(path))
	if err != nil {
		return "", fmt.Errorf("read recovery key file %s: %w", path, err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", fmt.Errorf("recovery key file %s is empty", path)
	}
	return key, nil
}

func writeRecoveryKeyFile(path, key string) error {
	path = expandHome(path)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("recovery key file already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat recovery key file %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		return fmt.Errorf("write recovery key file %s: %w", path, err)
	}
	return nil
}

// parseServerRemap parses "34:8,35:9" into {"34":8,"35":9}. The map key is the
// source server ID (string, matching the API's server_remap shape) and the
// value is the target server ID.
func parseServerRemap(s string) (map[string]int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	out := make(map[string]int64)
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid --server-remap entry %q (want src:dst)", pair)
		}
		src := strings.TrimSpace(parts[0])
		dst, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid target server id in %q: %w", pair, err)
		}
		if _, err := strconv.ParseInt(src, 10, 64); err != nil {
			return nil, fmt.Errorf("invalid source server id in %q: %w", pair, err)
		}
		out[src] = dst
	}
	return out, nil
}
