// myserver backup — system-level backup management.
//
// Two storage modes:
//   - Local (default for "just give me a download"): backup writes
//     to the platform host's disk at /var/lib/myserver/backups by
//     default. Survives only as long as the host. Download via
//     `myserver backup download` streams the file to your laptop.
//   - S3 (recommended for DR): backup writes to a configured S3
//     bucket. Pass --s3-storage-id=N (configure storages in the web
//     UI first).
//
// System backups require system_admin role. The CLI surfaces a 403
// with a clear message if the logged-in user isn't an admin.

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

func runBackup(args []string) error {
	if len(args) == 0 {
		backupUsage()
		return fmt.Errorf("no subcommand specified")
	}
	switch args[0] {
	case "list", "ls":
		return runBackupList(args[1:])
	case "create":
		return runBackupCreate(args[1:])
	case "download":
		return runBackupDownload(args[1:])
	case "-h", "--help", "help":
		backupUsage()
		return nil
	default:
		backupUsage()
		return fmt.Errorf("unknown backup subcommand %q", args[0])
	}
}

func backupUsage() {
	fmt.Fprintln(os.Stderr, "Usage: myserver backup <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  list      List system backups.")
	fmt.Fprintln(os.Stderr, "  create    Trigger a new system backup (local or S3).")
	fmt.Fprintln(os.Stderr, "  download  Stream a backup file to local disk.")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "System backups require system_admin role.")
}

func runBackupList(args []string) error {
	fs := flag.NewFlagSet("backup list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to your only team)")
	apiURL := fs.String("api", "", "myserver API URL")
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
	backups, err := api.listSystemBackups()
	if err != nil {
		return fmt.Errorf("list backups: %w", err)
	}
	if len(backups) == 0 {
		fmt.Fprintln(os.Stderr, "(no system backups)")
		return nil
	}
	for _, b := range backups {
		mode := "s3"
		dest := "-"
		if b.S3StorageID != nil {
			dest = fmt.Sprintf("storage=%d", *b.S3StorageID)
		} else if b.LocalDiskPath != "" {
			mode = "local"
			dest = b.LocalDiskPath
		}
		size := "-"
		if b.Size != nil {
			size = humanBytes(*b.Size)
		}
		filename := "-"
		if b.Filename != nil {
			filename = *b.Filename
		}
		fmt.Printf("%d\t%s\t%s\t%s\t%s\t%s\n",
			b.ID, b.Status, mode, dest, size, filename)
	}
	return nil
}

// runBackupCreate handles `myserver backup create`. Defaults to local
// mode (writes to /var/lib/myserver/backups on the platform host).
// Pass --s3-storage-id=N to write to a configured S3 bucket instead.
func runBackupCreate(args []string) error {
	fs := flag.NewFlagSet("backup create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id (defaults to your only team)")
	s3StorageID := fs.Int64("s3-storage-id", 0, "ID of a configured S3 storage (omit for local-mode backup)")
	localPath := fs.String("local-path", "/var/lib/myserver/backups", "host directory for local-mode backups (default: /var/lib/myserver/backups)")
	includeDBData := fs.Bool("include-db-data", true, "also dump customer database contents (S3 mode only)")
	apiURL := fs.String("api", "", "myserver API URL")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: myserver backup create [--s3-storage-id=<id>] [--local-path=<path>]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Modes (pick one):")
		fmt.Fprintln(os.Stderr, "  Default: local-mode backup at /var/lib/myserver/backups on the platform host.")
		fmt.Fprintln(os.Stderr, "           Survives only as long as the host. Download via `backup download`.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  --s3-storage-id=<id>   write to a pre-configured S3 storage destination.")
		fmt.Fprintln(os.Stderr, "                          See web UI → Settings → Storage to register an S3 bucket.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Optional:")
		fmt.Fprintln(os.Stderr, "  --local-path=<path>    override local backup dir (default /var/lib/myserver/backups)")
		fmt.Fprintln(os.Stderr, "  --include-db-data      also dump customer database contents (S3 mode only)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Requires system_admin role.")
	}

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

	req := CreateSystemBackupRequest{
		IncludeDBData: includeDBData,
	}
	if *s3StorageID > 0 {
		req.S3StorageID = *s3StorageID
		fmt.Fprintf(os.Stderr, "▸ Creating S3 backup (storage_id=%d)\n", *s3StorageID)
	} else {
		req.LocalDiskPath = strings.TrimSpace(*localPath)
		fmt.Fprintf(os.Stderr, "▸ Creating LOCAL backup at %s on the platform host\n", req.LocalDiskPath)
		fmt.Fprintln(os.Stderr, "  ⚠ Local backups die with the host. Configure S3 for real DR.")
	}

	b, err := api.createSystemBackup(req)
	if err != nil {
		return fmt.Errorf("create backup: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Backup #%d enqueued (status=%s)\n", b.ID, b.Status)
	fmt.Fprintln(os.Stderr, "  Build runs in the background. Check status with `myserver backup list`.")
	fmt.Fprintln(os.Stderr, "  Once status=completed, download with:")
	fmt.Fprintf(os.Stderr, "    myserver backup download --id=%d\n", b.ID)
	// stdout: id, for scripting
	fmt.Println(b.ID)
	return nil
}

// runBackupDownload streams a completed backup file to local disk.
// Auto-resolves the filename from the backup record so the caller
// only needs --id. Default output is ./backup-<id>-<filename>.
func runBackupDownload(args []string) error {
	fs := flag.NewFlagSet("backup download", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	teamID := fs.Int64("team", 0, "team id")
	id := fs.Int64("id", 0, "backup id (required, see `myserver backup list`)")
	output := fs.String("output", "", "local path to write to (default: ./<backup-filename>)")
	apiURL := fs.String("api", "", "myserver API URL")
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

	// List to find the backup's filename — the download endpoint
	// needs both id + filename, but the CLI shouldn't make the user
	// type that.
	backups, err := api.listSystemBackups()
	if err != nil {
		return fmt.Errorf("list backups: %w", err)
	}
	var target *SystemBackup
	for i := range backups {
		if backups[i].ID == *id {
			target = &backups[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("backup %d not found", *id)
	}
	if target.Filename == nil || *target.Filename == "" {
		return fmt.Errorf("backup %d has no filename — status=%s, possibly still building or failed", *id, target.Status)
	}
	if target.Status != "completed" {
		return fmt.Errorf("backup %d not completed (status=%s) — wait for build to finish", *id, target.Status)
	}

	outPath := *output
	if outPath == "" {
		outPath = "./" + *target.Filename
	}
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("open output file %s: %w", outPath, err)
	}
	defer f.Close()

	fmt.Fprintf(os.Stderr, "▸ Downloading backup #%d → %s\n", *id, outPath)
	if err := api.downloadSystemBackup(*id, *target.Filename, f); err != nil {
		os.Remove(outPath)
		return err
	}
	info, _ := f.Stat()
	fmt.Fprintf(os.Stderr, "✓ Downloaded %s (%s)\n", outPath, humanBytes(info.Size()))
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Note: file is AES-256-GCM encrypted with the platform's APP_SECRET-derived key.")
	fmt.Fprintln(os.Stderr, "To restore, import via the same myserver instance (or one with the same APP_SECRET).")
	return nil
}

// humanBytes formats a byte count for display. Used by list + download
// summaries — backups can be MBs to GBs, raw byte counts are unreadable.
func humanBytes(n int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case n >= GB:
		return fmt.Sprintf("%.2fGB", float64(n)/float64(GB))
	case n >= MB:
		return fmt.Sprintf("%.2fMB", float64(n)/float64(MB))
	case n >= KB:
		return fmt.Sprintf("%.2fKB", float64(n)/float64(KB))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
