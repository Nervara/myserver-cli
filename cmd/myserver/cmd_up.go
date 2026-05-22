package main

// `myserver up` — local→prod deploy. Replaces the old rsync+ssh shim.
//
// What it ships in BOTH paths: a gzipped SOURCE TARBALL of the project
// root (NOT a Docker image — no `docker save`, no local Docker daemon).
// Exclusions: node_modules, dist, .git, .env*, and anything matched
// by .myserverignore / .dockerignore.
//
// The build pack picks which endpoint receives the tarball and what
// the server does with it:
//
//   build_pack=dockerimage  → POST /applications/{id}/build-tarball
//       Server extracts on the build target, runs `docker build`,
//       pushes the resulting image to localhost:5050, streams build
//       output back as text/plain. We then PATCH the app's
//       docker_registry_image_{name,tag} and POST /deploy.
//
//   anything else           → POST /applications/{id}/source-tarball
//       Server extracts on the build target and creates the
//       deployment with SourceTarballPath set. The deploy pipeline's
//       clone stage skips git-clone (source already present) and
//       hands off to the build pack (railpack auto-detects /
//       dockerfile runs your Dockerfile / dockercompose runs the
//       compose stack / static serves the files). Image fields are
//       owned by the pipeline; no PATCH from the CLI.
//
// Either way: no SSH from the laptop, no Docker on the laptop. The
// only outbound traffic is HTTPS to the myserver API.

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

func runUp(args []string) error {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	skipBuild := fs.Bool("skip-build", false, "skip pack+upload+build, just trigger redeploy of last image")
	dryRun := fs.Bool("dry-run", false, "print what would run without executing")
	noFollow := fs.Bool("no-follow", false, "return immediately after enqueueing the deploy (don't tail logs)")
	tagFlag := fs.String("tag", "", "override image tag (default: short git sha or unix ts)")
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
	if pc.AppID == 0 {
		return fmt.Errorf("project config missing app_id — re-run `myserver init`")
	}
	api := newAPI(creds, pc.TeamID)

	// Build-pack determines the deploy path:
	//
	//   dockerimage  → /build-tarball (build + push image inline, then patch + /deploy)
	//   anything else → /source-tarball (upload source, pipeline builds it)
	//
	// `--skip-build` only makes sense for dockerimage; for source uploads
	// there's no pre-built image to reuse. Surface that intent up-front.
	if pc.BuildPack != "dockerimage" && *skipBuild {
		return fmt.Errorf("--skip-build only applies to dockerimage build pack; this app uses %q", pc.BuildPack)
	}
	if pc.BuildPack != "dockerimage" && *tagFlag != "" {
		fmt.Fprintf(os.Stderr, "  note: --tag is ignored for %s build pack (image tag set by pipeline)\n", pc.BuildPack)
	}

	// First-deploy ergonomics: a freshly-created app has no FQDN until
	// somebody calls generate-fqdn. We do that automatically here so
	// `myserver app create … && myserver up` produces a reachable URL
	// without forcing the customer to know about the extra step.
	// Idempotent: skipped when the app already has a FQDN.
	if !*dryRun {
		if err := ensureFQDN(api, pc); err != nil {
			return err
		}
	}

	if pc.BuildPack != "dockerimage" {
		return runUpFromSource(api, pc, *dryRun, *noFollow)
	}
	return runUpFromDockerImage(api, pc, *tagFlag, *skipBuild, *dryRun, *noFollow)
}

// ensureFQDN auto-generates a public hostname for the bound app if one isn't
// set yet. No-op when the app already has a FQDN. Logs the chosen URL so
// the customer can copy/paste it after deploy.
func ensureFQDN(api *apiClient, pc *ProjectConfig) error {
	// Cached pc.FQDN is best-effort; the source of truth is the API.
	// Re-fetch so a stale pc (e.g. from `myserver init` before a UI edit)
	// doesn't trigger an unwanted re-generate.
	app, err := api.getApp(pc.AppID)
	if err != nil {
		return fmt.Errorf("look up app %d: %w", pc.AppID, err)
	}
	if strings.TrimSpace(app.FQDN) != "" {
		return nil
	}
	fmt.Fprintln(os.Stderr, "▸ no FQDN set — generating one")
	updated, err := api.generateFQDN(pc.AppID)
	if err != nil {
		return fmt.Errorf("generate FQDN: %w", err)
	}
	fqdn := strings.TrimSpace(updated.FQDN)
	if fqdn == "" {
		// Server returned 200 but didn't populate FQDN — surface clearly
		// rather than silently moving on to a deploy that has no URL.
		return fmt.Errorf("server didn't return a FQDN (no base domain configured? add one in Settings → Domains)")
	}
	fmt.Fprintf(os.Stderr, "  url: %s\n", strings.SplitN(fqdn, ",", 2)[0])
	pc.FQDN = fqdn
	if err := saveProjectConfig(pc); err != nil {
		// Non-fatal: the FQDN is already saved server-side. Just warn.
		fmt.Fprintf(os.Stderr, "  warn: couldn't update local %s with new FQDN: %v\n", projectConfigFn, err)
	}
	return nil
}

// runUpFromDockerImage implements `myserver up` for the dockerimage build
// pack: pack cwd → POST /build-tarball (which builds + pushes) → PATCH the
// app's image fields → POST /deploy. This is the original (only) flow.
func runUpFromDockerImage(api *apiClient, pc *ProjectConfig, tagFlag string, skipBuild, dryRun, noFollow bool) error {
	tag := tagFlag
	if tag == "" {
		if sha, err := gitShortSHA(); err == nil && sha != "" {
			tag = sha
		} else {
			tag = fmt.Sprintf("%d", time.Now().Unix())
		}
	}

	imageRepo, imageTag := "", ""
	if !skipBuild {
		fmt.Fprintln(os.Stderr, "▸ packing source")
		if dryRun {
			fmt.Fprintln(os.Stderr, "  [dry-run] would tar+gzip cwd, post to /build-tarball, parse OK line")
		} else {
			pr, pw := io.Pipe()
			done := make(chan error, 1)
			go func() {
				files, bytes, err := writeTarball(".", pw)
				if err == nil {
					fmt.Fprintf(os.Stderr, "  packed %d files, %.1fMB uncompressed\n",
						files, float64(bytes)/(1<<20))
				}
				done <- err
				_ = pw.Close()
			}()
			fmt.Fprintln(os.Stderr, "▸ uploading + building remotely")
			repo, tg, err := api.buildTarball(pc.AppID, tag, pr, func(line string) {
				fmt.Println(line)
			})
			if perr := <-done; perr != nil && err == nil {
				err = perr
			}
			if err != nil {
				return err
			}
			imageRepo, imageTag = repo, tg
		}
	} else {
		// On --skip-build we still need an image_name and tag for the
		// PATCH. Use whatever's currently on the app.
		app, err := api.getApp(pc.AppID)
		if err != nil {
			return err
		}
		imageRepo, imageTag = app.DockerRegistryImageName, app.DockerRegistryImageTag
		if imageRepo == "" || imageTag == "" {
			return fmt.Errorf("--skip-build needs the app to already have an image; none set")
		}
	}

	if !dryRun {
		fmt.Fprintf(os.Stderr, "▸ patching app: %s -> %s\n", imageRepo, imageTag)
		if err := api.patchApp(pc.AppID, map[string]any{
			"docker_registry_image_name": imageRepo,
			"docker_registry_image_tag":  imageTag,
		}); err != nil {
			return err
		}
	}

	fmt.Fprintln(os.Stderr, "▸ triggering deploy")
	if dryRun {
		fmt.Fprintf(os.Stderr, "  [dry-run] POST /api/v1/applications/%d/deploy\n", pc.AppID)
		return nil
	}
	dep, err := api.deployApp(pc.AppID)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "  deployment %d enqueued (status=%s)\n", dep.ID, dep.Status)
	if noFollow {
		return nil
	}
	return tailDeployment(api, pc.AppID, dep.ID)
}

// runUpFromSource implements `myserver up` for non-dockerimage build packs.
// One round trip: pack cwd, stream to /source-tarball; the server extracts
// on the build target, creates the deployment with SourceTarballPath set,
// and enqueues the worker task. Pipeline's clone stage skips git-clone
// because the source is already there.
//
// No image patch: pipeline builds the image from source (railpack /
// dockerfile / dockercompose / static), so the app's image fields are
// owned by the pipeline, not the CLI.
func runUpFromSource(api *apiClient, pc *ProjectConfig, dryRun, noFollow bool) error {
	fmt.Fprintln(os.Stderr, "▸ packing source")
	if dryRun {
		fmt.Fprintln(os.Stderr, "  [dry-run] would tar+gzip cwd, POST /source-tarball, then tail deploy logs")
		return nil
	}

	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		files, bytes, err := writeTarball(".", pw)
		if err == nil {
			fmt.Fprintf(os.Stderr, "  packed %d files, %.1fMB uncompressed\n",
				files, float64(bytes)/(1<<20))
		}
		done <- err
		_ = pw.Close()
	}()

	fmt.Fprintln(os.Stderr, "▸ uploading source + creating deployment")
	dep, err := api.sourceTarball(pc.AppID, pr)
	if perr := <-done; perr != nil && err == nil {
		err = perr
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "  deployment %d enqueued (status=%s)\n", dep.ID, dep.Status)
	if noFollow {
		return nil
	}
	return tailDeployment(api, pc.AppID, dep.ID)
}

func gitShortSHA() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--short=12", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Surfaced separately so cmd_logs can re-use it.
func tailDeployment(api *apiClient, appID, deployID int64) error {
	fmt.Fprintln(os.Stderr, "▸ tailing logs (Ctrl+C to stop following — deploy continues)")
	seen := 0
	terminal := map[string]bool{"finished": true, "failed": true, "cancelled": true}
	for {
		logs, err := api.deploymentLogs(appID, deployID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warn: fetch logs: %v\n", err)
		}
		for i := seen; i < len(logs); i++ {
			line := logs[i]
			prefix := "·"
			switch line.Src {
			case "stderr":
				prefix = "!"
			case "builder":
				prefix = "▸"
			}
			fmt.Printf("%s %s\n", prefix, strings.TrimRight(line.Msg, "\n"))
		}
		if len(logs) > seen {
			seen = len(logs)
		}
		dep, err := api.getDeployment(appID, deployID)
		if err != nil {
			return err
		}
		if terminal[dep.Status] {
			fmt.Fprintf(os.Stderr, "▸ deployment %s: %s\n", dep.Status, strOrDash(dep.Error))
			if dep.Status == "failed" {
				return fmt.Errorf("deployment failed")
			}
			return nil
		}
		time.Sleep(2 * time.Second)
	}
}

func strOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
