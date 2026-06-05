// MCP tool definitions + dispatch.
//
// Each tool is a thin wrapper around an apiClient method, with:
//   - a JSON Schema input definition (so the AI knows the shape)
//   - a hint annotation (readOnly / destructive) the client can use to
//     decide whether to ask for confirmation before invoking
//   - a handler that returns a human-readable string (the AI sees this)
//
// Adding a tool: define a new toolSpec in mcpTools, implement its handler,
// and the dispatch + tools/list endpoints pick it up automatically.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// toolSpec is what we publish via tools/list and dispatch on for tools/call.
type toolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
	handler     func(api *apiClient, args json.RawMessage) (string, error)
}

// schema is a tiny constructor for an object schema with required fields.
func schema(props map[string]any, required ...string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// readOnlyHint and destructiveHint are MCP annotations clients use to decide
// whether a tool needs a confirmation step before being invoked.
func readOnly() map[string]any {
	return map[string]any{"readOnlyHint": true}
}
func destructive() map[string]any {
	return map[string]any{"destructiveHint": true}
}

// mcpTools is the source of truth for the MCP server's tool surface.
var mcpTools = []toolSpec{
	// --- discovery / orientation ---
	{
		Name:        "whoami",
		Description: "Identify the current user and the team this MCP session is scoped to. Use this first if you're unsure which team's resources you'll be acting on.",
		InputSchema: schema(map[string]any{}),
		Annotations: readOnly(),
		handler:     handleWhoami,
	},
	{
		Name:        "register_user",
		Description: "Register a new myserver user through the public /auth/register endpoint. Use for self-hosted first-user/admin bootstrap or when registration is intentionally enabled. Mutating: creates a user and personal team. Required: name, email, password. Optional: timezone. Does not return tokens to avoid leaking credentials through MCP transcripts; after registration, ask the user to run `myserver login` or use the CLI `myserver auth register` if they want credentials saved locally.",
		InputSchema: schema(
			map[string]any{
				"name":     map[string]any{"type": "string", "description": "Display name for the new user"},
				"email":    map[string]any{"type": "string", "description": "Email address for the new user"},
				"password": map[string]any{"type": "string", "description": "Password for the new user. Registration may reject weak passwords."},
				"timezone": map[string]any{"type": "string", "description": "Optional IANA timezone identifier, e.g. Europe/Dublin"},
			},
			"name", "email", "password",
		),
		Annotations: destructive(),
		handler:     handleRegisterUser,
	},
	{
		Name:        "list_projects",
		Description: "List all projects in the active team. Projects contain environments which contain apps and databases.",
		InputSchema: schema(map[string]any{}),
		Annotations: readOnly(),
		handler:     handleListProjects,
	},
	{
		Name:        "list_environments",
		Description: "List environments inside a project (e.g. production, staging). Most create operations need an environment_id.",
		InputSchema: schema(
			map[string]any{
				"project_id": map[string]any{"type": "integer", "description": "Project ID"},
			},
			"project_id",
		),
		Annotations: readOnly(),
		handler:     handleListEnvironments,
	},
	// --- apps ---
	{
		Name:        "detect_build_pack",
		Description: "Inspect a local directory and return the recommended build_pack value for `create_app`. Use BEFORE create_app when the user says 'create an app from this project' / 'deploy this directory' so you don't have to guess. Detection: docker-compose.yml or compose.yaml → 'dockercompose'; Dockerfile → 'dockerfile'; index.html with no language manifests → 'static'; everything else → 'railpack' (which then auto-detects Node/Python/Go/Rust/Java/Ruby server-side). The returned value drops straight into create_app's build_pack field.",
		InputSchema: schema(
			map[string]any{
				"path": map[string]any{"type": "string", "description": "Absolute or relative directory to inspect. Defaults to the working directory the MCP server was launched from."},
			},
		),
		Annotations: readOnly(),
		handler:     handleDetectBuildPack,
	},
	{
		Name:        "generate_fqdn",
		Description: "Auto-assign a public hostname to an app that has none. Use this on a freshly-created app, or when get_app shows fqdn is empty and the user wants a URL they can hit. The server picks a sensible default (sslip.io for raw IPs, or a team-registered wildcard if configured). Idempotent: re-calling on an app that already has an FQDN replaces it with a new generated one (rarely what the user wants — check get_app first). ⚠️ TIMING: Call this BEFORE the first deploy_app / `myserver up`. If you generate an FQDN AFTER a deploy completes, the Caddy proxy doesn't know about it until the NEXT deploy — you'll see 502s on the new FQDN until you redeploy. Workflow: create_app → generate_fqdn → deploy.",
		InputSchema: schema(
			map[string]any{
				"app_id": map[string]any{"type": "integer", "description": "Application ID"},
			},
			"app_id",
		),
		Annotations: destructive(),
		handler:     handleGenerateFQDN,
	},
	{
		Name:        "create_app",
		Description: "Use when the user wants to register a NEW application in a team's environment (first-time onboarding, scaffolding a fresh project). Required: name, environment_id, build_pack. Common: git_repository, fqdn, ports_exposes, server_id. Build-pack-specific fields cover Dockerfile / docker-compose / docker-image flows. After creation, the next steps are: bind a working directory with `myserver init --app=<id>` and deploy with `myserver up` (or call the `set_env_var` tool first if env vars are needed). Do NOT use this to redeploy an existing app — call `deploy` MCP tools for that.\n\nIF THE APP NEEDS TO CALL MYSERVER AT RUNTIME (e.g. multi-tenant SaaS adding customer custom domains, scheduled exports, programmatic redeploys): after create_app, also call create_app_token with auto_inject=true and the appropriate scopes. The next deploy will inject MYSERVER_API_URL / MYSERVER_APP_ID / MYSERVER_APP_TOKEN env vars. Use app_runtime_env to see the full runtime contract for an app while writing code against it. For the full multi-tenant-custom-domain blueprint (code snippets + DNS instructions you must show the customer's tenants), call saas_custom_domain_recipe first.",
		InputSchema: schema(
			map[string]any{
				"name":                       map[string]any{"type": "string", "description": "Application name (alphanumeric + dashes, lowercase preferred)"},
				"environment_id":             map[string]any{"type": "integer", "description": "Target environment ID — find via list_environments"},
				"build_pack":                 map[string]any{"type": "string", "enum": []string{"railpack", "dockerfile", "dockercompose", "static", "dockerimage"}, "description": "How myserver builds the app. railpack auto-detects the language (Node/Python/Go/.NET/Ruby/Java) and builds; dockerfile uses the project's Dockerfile; dockercompose runs the docker-compose.yml stack; static serves a static-site bundle. dockerimage = deploy a Docker image. Two ways to feed dockerimage: (a) set docker_registry_image_name + docker_registry_image_tag to an existing image and the pipeline just pulls + runs it; or (b) run `myserver up` from a directory with a Dockerfile and the server runs `docker build` on the build target, pushes to localhost:5050, then deploys. The `myserver up` flow NEVER needs Docker on the customer's laptop — it ships a source tarball, not a pre-built image."},
				"description":                map[string]any{"type": "string"},
				"git_repository":             map[string]any{"type": "string", "description": "Git URL — required for build-packs that build from source (nixpacks/dockerfile/dockercompose/static/s3static)"},
				"git_branch":                 map[string]any{"type": "string", "description": "Defaults to 'main'"},
				"fqdn":                       map[string]any{"type": "string", "description": "Public hostname (e.g. 'blog.example.com'); leave empty for non-HTTP apps"},
				"ports_exposes":              map[string]any{"type": "string", "description": "Container's INTERNAL listen port as a string (e.g. '3000'). Must match what the app actually listens on, not the host port."},
				"server_id":                  map[string]any{"type": "integer", "description": "Pin to a specific server; leave unset for the team's default"},
				"dockerfile_location":        map[string]any{"type": "string", "description": "Path to Dockerfile inside the repo (only for build_pack=dockerfile)"},
				"docker_compose_location":    map[string]any{"type": "string", "description": "Path to docker-compose.yml inside the repo (only for build_pack=dockercompose)"},
				"docker_registry_image_name": map[string]any{"type": "string", "description": "Docker image name (only for build_pack=docker-image)"},
				"docker_registry_image_tag":  map[string]any{"type": "string", "description": "Docker image tag (defaults to 'latest' for build_pack=docker-image)"},
				"static_image":               map[string]any{"type": "string", "description": "Base image for static-site serving (only for build_pack=static)"},
			},
			"name", "environment_id", "build_pack",
		),
		Annotations: destructive(),
		handler:     handleCreateApp,
	},
	{
		Name:        "update_app",
		Description: "Patch an existing application's config. Use to: switch build_pack on a stuck app, clear a stale docker image so the pipeline rebuilds from source, change FQDN, hand off from a manual build to a git repo. Pass empty string ('') to CLEAR a field — important when switching from dockerimage to another build_pack: also clear docker_registry_image_name and docker_registry_image_tag, otherwise the pipeline keeps pulling the cached image (the IsDockerImage() check looks at image_name, not build_pack). Only the fields you specify are sent; everything else is left alone. NOT for redeploys — call deploy_app for that.",
		InputSchema: schema(
			map[string]any{
				"app_id":                     map[string]any{"type": "integer"},
				"name":                       map[string]any{"type": "string", "description": "Rename the app"},
				"build_pack":                 map[string]any{"type": "string", "enum": []string{"railpack", "dockerfile", "dockercompose", "static", "dockerimage"}},
				"git_repository":             map[string]any{"type": "string", "description": "Git URL; '' to clear"},
				"git_branch":                 map[string]any{"type": "string"},
				"fqdn":                       map[string]any{"type": "string", "description": "Public hostname; '' to clear"},
				"ports_exposes":              map[string]any{"type": "string", "description": "Container internal listen port"},
				"dockerfile_location":        map[string]any{"type": "string"},
				"docker_compose_location":    map[string]any{"type": "string"},
				"docker_registry_image_name": map[string]any{"type": "string", "description": "'' to clear (do this when leaving the dockerimage build pack)"},
				"docker_registry_image_tag":  map[string]any{"type": "string", "description": "'' to clear (do this when leaving the dockerimage build pack)"},
				"static_image":               map[string]any{"type": "string"},
			},
			"app_id",
		),
		Annotations: destructive(),
		handler:     handleUpdateApp,
	},
	{
		Name:        "list_apps",
		Description: "List all applications visible to the active team.",
		InputSchema: schema(map[string]any{}),
		Annotations: readOnly(),
		handler:     handleListApps,
	},
	{
		Name:        "get_app",
		Description: "Fetch one application by its numeric ID. Returns name, build pack, status, FQDN, destination server.",
		InputSchema: schema(
			map[string]any{
				"id": map[string]any{"type": "integer", "description": "Application ID"},
			},
			"id",
		),
		Annotations: readOnly(),
		handler:     handleGetApp,
	},
	{
		Name:        "list_servers",
		Description: "List all servers registered in the active team.",
		InputSchema: schema(map[string]any{}),
		Annotations: readOnly(),
		handler:     handleListServers,
	},
	{
		Name:        "list_deployments",
		Description: "List recent deployments for an application, newest first. Default limit is 5. Use this to verify a `myserver up` / deploy_app actually finished. Status meanings: queued (just enqueued, not picked up yet), in_progress (worker is building/deploying), finished (success — container should be live), failed (build or deploy error — call tail_deployment_logs to see why). A 'finished' status only means the pipeline completed; it does NOT guarantee the container is actually running (see cutover-orphan note on deploy_app). Always cross-check with get_app (status field) + tail_app_logs (404 = no container) after a deploy.",
		InputSchema: schema(
			map[string]any{
				"app_id": map[string]any{"type": "integer", "description": "Application ID"},
				"limit":  map[string]any{"type": "integer", "description": "Max number of deployments (default 5)"},
			},
			"app_id",
		),
		Annotations: readOnly(),
		handler:     handleListDeployments,
	},
	{
		Name:        "tail_deployment_logs",
		Description: "Fetch the BUILD/DEPLOY log lines for one specific deployment (the asynq worker's output as it ran the 8-stage pipeline: clone → env-resolve → build → push → pull → deploy → health → post-hooks). Different from tail_app_logs which is the live container's stdout/stderr. Use this when list_deployments shows status=failed, or when a deploy is stuck in 'in_progress' for a long time. Common failure signatures: 'no such file or directory' in clone stage = missing file (often a .dockerignore mistake); 'failed to fetch' in build stage = network or upstream registry issue; healthcheck timeout = container didn't bind the expected port (check ports_exposes matches the app's actual listen port).",
		InputSchema: schema(
			map[string]any{
				"app_id":        map[string]any{"type": "integer"},
				"deployment_id": map[string]any{"type": "integer"},
			},
			"app_id", "deployment_id",
		),
		Annotations: readOnly(),
		handler:     handleTailDeploymentLogs,
	},
	{
		Name:        "tail_app_logs",
		Description: "Fetch recent RUNTIME container logs for an application (NOT deploy logs — use tail_deployment_logs for build/deploy output). Use for diagnosing live-running issues (crashes, request errors, runtime exceptions). Returns the last N lines (default 200, max 5000). ⚠️ A 404 'no container found' response is a SIGNAL, not just an error: it means the deployment completed but the container isn't actually running. Possible causes: (1) cutover-orphan bug — most common after a deploy_app on a dockerimage app right after `myserver up` (workaround: re-run `myserver up`), (2) container crashed immediately after start (check tail_deployment_logs final stage for healthcheck failure), (3) stop_app was called and not restarted. If get_app says status=running but tail_app_logs returns 404, trust the 404 — status field can lag.",
		InputSchema: schema(
			map[string]any{
				"app_id": map[string]any{"type": "integer", "description": "Application ID"},
				"lines":  map[string]any{"type": "integer", "description": "Max lines to return (1-5000, default 200)"},
			},
			"app_id",
		),
		Annotations: readOnly(),
		handler:     handleTailAppLogs,
	},
	// --- env vars ---
	{
		Name:        "list_env_vars",
		Description: "List environment variables defined on an application. Values for non-literal entries are returned encrypted/redacted by the API — use this for inspection, not secret retrieval.",
		InputSchema: schema(
			map[string]any{
				"app_id": map[string]any{"type": "integer", "description": "Application ID"},
			},
			"app_id",
		),
		Annotations: readOnly(),
		handler:     handleListEnvVars,
	},
	{
		Name:        "set_env_var",
		Description: "Create a new environment variable on an application. Defaults to runtime-only and literal=false (encrypted at rest). For an UPDATE of an existing key, delete and re-create.\n\nDO NOT set these keys manually — the deploy pipeline auto-injects them, and explicit user-set values WIN (overriding the platform's): MYSERVER_API_URL, MYSERVER_APP_ID, MYSERVER_APP_TOKEN (when an auto_inject app token exists), LOG_INGEST_URL, LOG_INGEST_TOKEN (when platform logging is enabled). Setting DATABASE_URL manually IS expected when you have a SQLite resource — see app_runtime_env for the per-driver format you need.",
		InputSchema: schema(
			map[string]any{
				"app_id":       map[string]any{"type": "integer"},
				"key":          map[string]any{"type": "string", "description": "Variable name (typically uppercase)"},
				"value":        map[string]any{"type": "string"},
				"is_literal":   map[string]any{"type": "boolean", "description": "If true the value is stored as-is (not encrypted) — use only for non-secret values like URLs"},
				"is_runtime":   map[string]any{"type": "boolean", "description": "Available to running container (default true)"},
				"is_buildtime": map[string]any{"type": "boolean", "description": "Available during build (default false)"},
			},
			"app_id", "key", "value",
		),
		Annotations: destructive(),
		handler:     handleSetEnvVar,
	},
	{
		Name:        "delete_env_var",
		Description: "Delete one environment variable by its ID (find via list_env_vars). Reversible only by re-creating with the same key + value.",
		InputSchema: schema(
			map[string]any{
				"id": map[string]any{"type": "integer", "description": "Env var ID"},
			},
			"id",
		),
		Annotations: destructive(),
		handler:     handleDeleteEnvVar,
	},
	// --- managed databases (postgres / mysql / etc., not SQLite) ---
	{
		Name:        "list_databases",
		Description: "List all managed standalone databases (postgres / mysql / mariadb / mongodb / redis / clickhouse / keydb / dragonfly) in the active team.",
		InputSchema: schema(map[string]any{}),
		Annotations: readOnly(),
		handler:     handleListDatabases,
	},
	{
		Name:        "get_database",
		Description: "Fetch one managed database by ID. Returns name, type, image, environment, status, public/port flags.",
		InputSchema: schema(
			map[string]any{
				"id": map[string]any{"type": "integer", "description": "Database ID"},
			},
			"id",
		),
		Annotations: readOnly(),
		handler:     handleGetDatabase,
	},
	{
		Name:        "restart_database",
		Description: "Restart a managed database container. Mutating but reversible — comes back with the same image and same volume.",
		InputSchema: schema(
			map[string]any{
				"id": map[string]any{"type": "integer", "description": "Database ID"},
			},
			"id",
		),
		Annotations: destructive(),
		handler:     handleRestartDatabase,
	},
	{
		Name:        "start_database",
		Description: "Start a stopped managed database container.",
		InputSchema: schema(
			map[string]any{
				"id": map[string]any{"type": "integer", "description": "Database ID"},
			},
			"id",
		),
		Annotations: destructive(),
		handler:     handleStartDatabase,
	},
	{
		Name:        "stop_database",
		Description: "Stop a running managed database container. Container stays around (use restart_database to bring it back); the volume is untouched.",
		InputSchema: schema(
			map[string]any{
				"id": map[string]any{"type": "integer", "description": "Database ID"},
			},
			"id",
		),
		Annotations: destructive(),
		handler:     handleStopDatabase,
	},
	// --- SQLite resources ---
	{
		Name:        "list_sqlite_resources",
		Description: "List first-class SQLite resources (managed volume + env-var injection) attached to apps in the active team.",
		InputSchema: schema(map[string]any{}),
		Annotations: readOnly(),
		handler:     handleListSQLite,
	},
	{
		Name:        "get_sqlite_resource",
		Description: "Fetch one SQLite resource by ID. Returns name, attached app, file path, env var key, journal mode, connection string.",
		InputSchema: schema(
			map[string]any{
				"id": map[string]any{"type": "integer"},
			},
			"id",
		),
		Annotations: readOnly(),
		handler:     handleGetSQLite,
	},
	{
		Name:        "delete_sqlite_resource",
		Description: "Delete a SQLite resource. By default the backing volume (and the .db file) is preserved; pass delete_volume=true to wipe it permanently.",
		InputSchema: schema(
			map[string]any{
				"id":            map[string]any{"type": "integer"},
				"delete_volume": map[string]any{"type": "boolean", "description": "If true, also remove the persistent volume — destroys data. Default false."},
			},
			"id",
		),
		Annotations: destructive(),
		handler:     handleDeleteSQLite,
	},
	{
		Name:        "deploy_app",
		Description: "Trigger a new deployment of an existing app using its currently-configured source (git repo OR last-pushed registry image). Mutating: enqueues an asynq job that runs the 8-stage pipeline. Returns immediately with the new deployment_id — does NOT wait for build to finish.\n\nPOST-DEPLOY VERIFICATION WORKFLOW (always do all of this):\n  1. list_deployments(app_id, limit=5) — confirm status progresses queued → in_progress → finished\n  2. If status=failed: tail_deployment_logs(app_id, deployment_id) for the build error\n  3. If status=finished: tail_app_logs(app_id, lines=80) to confirm container is actually serving (404 here = container did not start, see tail_app_logs description)\n  4. curl the FQDN's /healthz or root path to confirm end-to-end\n\n⚠️ KNOWN BUG — cutover-orphan on dockerimage redeploys: calling deploy_app on a build_pack=dockerimage app immediately after a `myserver up` can stop the NEW container during orphan cleanup. Symptoms: FQDN→502, tail_app_logs→'no container found', get_app still says status=running. Workaround: re-run `myserver up` from the user's local directory (don't just retry deploy_app — same code path). Doesn't affect railpack/dockerfile/dockercompose/static.\n\nFor source-from-local-disk uploads, the user runs `myserver up` from their CLI; this MCP tool re-runs the SAME pipeline against whatever source the app is already configured with.\n\nIf the app has an auto_inject service token configured (see create_app_token), the deploy pipeline will splice MYSERVER_API_URL + MYSERVER_APP_ID + MYSERVER_APP_TOKEN into the container env at this stage. Call app_runtime_env after deploy to confirm what was injected.",
		InputSchema: schema(
			map[string]any{
				"id": map[string]any{"type": "integer", "description": "Application ID"},
			},
			"id",
		),
		Annotations: destructive(),
		handler:     handleDeployApp,
	},
	{
		Name:        "restart_app",
		Description: "Restart an application's containers on a given server. Mutating but reversible (containers come back with the same image).",
		InputSchema: schema(
			map[string]any{
				"id":        map[string]any{"type": "integer", "description": "Application ID"},
				"server_id": map[string]any{"type": "integer", "description": "Server to restart on"},
			},
			"id", "server_id",
		),
		Annotations: destructive(),
		handler:     handleRestartApp,
	},
	{
		Name:        "start_app",
		Description: "Start a stopped application's containers on a given server. Use after stop_app to bring the app back without a full deploy.",
		InputSchema: schema(
			map[string]any{
				"id":        map[string]any{"type": "integer", "description": "Application ID"},
				"server_id": map[string]any{"type": "integer", "description": "Server to start on"},
			},
			"id", "server_id",
		),
		Annotations: destructive(),
		handler:     handleStartApp,
	},
	{
		Name:        "stop_app",
		Description: "Stop a running application's containers on a given server. Containers are removed but image and config remain — start_app brings them back.",
		InputSchema: schema(
			map[string]any{
				"id":        map[string]any{"type": "integer", "description": "Application ID"},
				"server_id": map[string]any{"type": "integer", "description": "Server to stop on"},
			},
			"id", "server_id",
		),
		Annotations: destructive(),
		handler:     handleStopApp,
	},
	{
		Name:        "delete_app",
		Description: "Soft-delete an application. Cascade-removes deployments + logs, env vars, SQLite resources, and app service tokens for this app. Server-side containers are cleaned up asynchronously by the worker (application:cleanup-deleted task). NOT reversible from the API — the soft-delete sets deleted_at but listings skip it; a re-create with the same name gets a fresh row. Use with care.",
		InputSchema: schema(
			map[string]any{
				"id": map[string]any{"type": "integer", "description": "Application ID"},
			},
			"id",
		),
		Annotations: destructive(),
		handler:     handleDeleteApp,
	},
	{
		Name:        "app_runtime_env",
		Description: "DEVELOPER REFERENCE: What env vars will my container have at runtime?\n\nCall this when you're writing app code and need to know which environment variables myserver will populate. Returns:\n\n  • Auto-injected by pipeline (if configured):\n      MYSERVER_API_URL    — base URL to call myserver back at\n      MYSERVER_APP_ID     — your app's numeric id\n      MYSERVER_APP_TOKEN  — bearer token (mst_…) for /api/v1/applications/{id}/{domains,deployments,...}\n      LOG_INGEST_URL      — POST JSONL logs here\n      LOG_INGEST_TOKEN    — bearer token for log ingest\n  • SQLite attachments — file path inside the container, plus per-driver connection-string suggestions (sqlite:/// for Python, file:// for Go, jdbc:sqlite:// for Java). The env var is NOT auto-injected by design; pick the format your driver wants and set DATABASE_URL via set_env_var.\n  • Counts of user-defined env vars (use list_env_vars for keys+values).\n\nTo enable MYSERVER_APP_TOKEN auto-injection, mint an app service token with create_app_token(auto_inject=true). To enable LOG_INGEST_*, flip platform_logging_enabled via the UI (or the platform-logging tool when it ships).\n\nSafe to call repeatedly — read-only.",
		InputSchema: schema(
			map[string]any{
				"app_id": map[string]any{"type": "integer", "description": "Application ID"},
			},
			"app_id",
		),
		Annotations: readOnly(),
		handler:     handleAppRuntimeEnv,
	},
	{
		Name:        "saas_custom_domain_recipe",
		Description: "RECIPE / DESIGN-GUIDE for building a multi-tenant SaaS that lets its customers point their own domains (e.g. shop.acmecorp.com) at the SaaS app. Read this FIRST when the user describes a feature like 'my users should bring their own domain', 'add custom-domain support', 'tenants verify their own hostname', etc. — it returns the end-to-end blueprint so you don't have to assemble it from individual tool descriptions and accidentally generate code that uses a human-user PAT instead of a runtime app token. Optional app_id: if you pass the SaaS app's id, the recipe is hydrated with that app's hostname + concrete next-step commands. Otherwise returns the generic blueprint.",
		InputSchema: schema(
			map[string]any{
				"app_id": map[string]any{"type": "integer", "description": "Optional. If set, the recipe is filled in with this specific SaaS app's id + hostname."},
			},
		),
		Annotations: readOnly(),
		handler:     handleSaaSCustomDomainRecipe,
	},
	{
		Name:        "list_app_domains",
		Description: "List the public hostnames an application is currently reachable at. Use this to see what's wired (e.g. before adding a new custom domain, or to debug why a request isn't routing). Returns an empty list when the app has no FQDN configured.",
		InputSchema: schema(
			map[string]any{
				"app_id": map[string]any{"type": "integer", "description": "Application ID"},
			},
			"app_id",
		),
		Annotations: readOnly(),
		handler:     handleListAppDomains,
	},
	{
		Name:        "add_app_domain",
		Description: "Add a public hostname to an application. Caddy auto-issues a TLS cert (HTTP-01 by default) on first request to the new hostname.\n\nBefore calling: ensure DNS for the hostname points at the server (CNAME → existing FQDN, or A → server IP). Caddy's HTTP-01 challenge fails if DNS doesn't resolve to us yet.\n\nIdempotent: adding an existing hostname is a 200 no-op (NOT 409) so a verification retry loop can hit this safely.\n\nUse case: a multi-tenant SaaS calls this when one of its tenants verifies a custom domain. Pair with the auto-inject app service token so the app authenticates as itself rather than as a human user. If you're DESIGNING that feature (not just calling the endpoint), read saas_custom_domain_recipe first — it returns the whole end-to-end blueprint including the DNS instructions to show the tenant.",
		InputSchema: schema(
			map[string]any{
				"app_id":   map[string]any{"type": "integer", "description": "Application ID"},
				"hostname": map[string]any{"type": "string", "description": "Hostname to add, e.g. 'example.com' or '*.tenant.example.com'"},
			},
			"app_id", "hostname",
		),
		Annotations: destructive(),
		handler:     handleAddAppDomain,
	},
	{
		Name:        "remove_app_domain",
		Description: "Remove a public hostname from an application. Idempotent — removing a non-existent hostname is a 204 no-op. REFUSES to remove the last hostname (an app with empty fqdn is unreachable; if that's truly what you want, use update_app with fqdn=''). Existing Caddy cert for the removed hostname stays in storage until expiry.",
		InputSchema: schema(
			map[string]any{
				"app_id":   map[string]any{"type": "integer", "description": "Application ID"},
				"hostname": map[string]any{"type": "string", "description": "Hostname to remove"},
			},
			"app_id", "hostname",
		),
		Annotations: destructive(),
		handler:     handleRemoveAppDomain,
	},
	{
		Name:        "create_app_token",
		Description: "Mint a new app service token (mst_…) for a deployed app. The returned plaintext is the ONLY copy that will ever exist — the response text contains it once and the user must save it. Use case: a multi-tenant SaaS app (like a multi-tenant SaaS) needs to call myserver's API at runtime to manage its own resources (add a customer custom domain, trigger a redeploy, etc.) without a human in the loop. Scopes are strict — pick the minimum the app needs. auto_inject=true makes the deploy pipeline splice MYSERVER_API_URL + MYSERVER_APP_ID + MYSERVER_APP_TOKEN env vars into the container on every deploy (at most one auto_inject token per app at a time). Without auto_inject, the customer must set MYSERVER_APP_TOKEN themselves via set_env_var. Allowed scopes: domains:read, domains:write, env:read, deploy:trigger, app:read.",
		InputSchema: schema(
			map[string]any{
				"app_id":      map[string]any{"type": "integer", "description": "Application ID"},
				"name":        map[string]any{"type": "string", "description": "Human label, e.g. 'myapp runtime'"},
				"scopes":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "e.g. ['domains:write','app:read']"},
				"auto_inject": map[string]any{"type": "boolean", "description": "Auto-inject as MYSERVER_APP_TOKEN env var on every deploy. At most one per app."},
			},
			"app_id", "name", "scopes",
		),
		Annotations: destructive(),
		handler:     handleCreateAppToken,
	},
	{
		Name:        "list_app_tokens",
		Description: "List app service tokens attached to an application. Plaintext is NEVER returned — only id, prefix, scopes, status. Use prefix + last_used_at to figure out which token an audit log line came from.",
		InputSchema: schema(
			map[string]any{
				"app_id": map[string]any{"type": "integer", "description": "Application ID"},
			},
			"app_id",
		),
		Annotations: readOnly(),
		handler:     handleListAppTokens,
	},
	{
		Name:        "revoke_app_token",
		Description: "Soft-delete a token by id. Idempotent — revoking an already-revoked token returns 204. Audit logs continue to point at the prefix even after revoke. For rotation: create a new token, redeploy (so MYSERVER_APP_TOKEN gets the new value), then revoke the old one.",
		InputSchema: schema(
			map[string]any{
				"id": map[string]any{"type": "integer", "description": "Token ID (see list_app_tokens)"},
			},
			"id",
		),
		Annotations: destructive(),
		handler:     handleRevokeAppToken,
	},
	{
		Name:        "create_sqlite_resource",
		Description: "Attach a managed SQLite database to an application. Creates a persistent Docker volume at the file's parent directory (e.g. /data) on the app's destination server, and auto-enqueues a redeploy so the volume mount takes effect. Single-replica apps only — SQLite has no replication.\n\n⚠️ NO ENV VAR AUTO-INJECTION (by design): creating the resource does NOT set DATABASE_URL or any env var on the app. This is deliberate — the right SQLite URL depends on the language/driver: Java JDBC wants 'jdbc:sqlite:/data/x.db', Python/SQLAlchemy wants 'sqlite:///data/x.db', Go database/sql wants 'file:/data/x.db?cache=shared', Bun/Node want the bare path. Picking one wrong by default has caused customer outages (Spring Boot HikariCP rejecting sqlite:/// URLs). The resource carries `env_var_key` + `connection_string` as suggestions; the customer chooses the format that fits their stack and calls set_env_var themselves.\n\nCorrect workflow: create_app → create_sqlite_resource → set_env_var(app_id, key='DATABASE_URL', value='<driver-specific>', is_literal=true) → deploy_app / `myserver up`. Without the set_env_var step, the volume mounts and the file path is reachable inside /data, but the app must hard-code or build the connection string from a known constant.",
		InputSchema: schema(
			map[string]any{
				"name":           map[string]any{"type": "string", "description": "Display name (e.g. \"primary\")"},
				"environment_id": map[string]any{"type": "integer"},
				"application_id": map[string]any{"type": "integer"},
				"file_path":      map[string]any{"type": "string", "description": "Absolute path inside the container (default /data/<name>.db)"},
				"env_var_key":    map[string]any{"type": "string", "description": "Env var to inject (default DATABASE_URL)"},
				"pragma_journal": map[string]any{"type": "string", "enum": []string{"wal", "delete"}, "description": "Default wal"},
			},
			"name", "environment_id", "application_id",
		),
		Annotations: destructive(),
		handler:     handleCreateSQLite,
	},
}

// mcpToolDescriptors strips the handler before we send the list to the client.
func mcpToolDescriptors() []map[string]any {
	out := make([]map[string]any, 0, len(mcpTools))
	for _, t := range mcpTools {
		entry := map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.InputSchema,
		}
		if len(t.Annotations) > 0 {
			entry["annotations"] = t.Annotations
		}
		out = append(out, entry)
	}
	return out
}

// dispatchTool routes a tools/call to the right handler.
func dispatchTool(api *apiClient, name string, args json.RawMessage) (string, error) {
	for _, t := range mcpTools {
		if t.Name == name {
			return t.handler(api, args)
		}
	}
	return "", fmt.Errorf("unknown tool: %s", name)
}

// --- handlers --------------------------------------------------------------

func handleDetectBuildPack(_ *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &p)
	}
	dir := p.Path
	if dir == "" {
		// MCP servers typically launch from the editor's working directory,
		// which is usually the project root. Fine default.
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get cwd: %w", err)
		}
	}
	bp := detectBuildPack(dir)
	// Surface the signal file the AI can quote back to the user — makes
	// the recommendation feel non-magical and gives them something to
	// override if it's wrong.
	signal := signalFileFor(dir, bp)
	if signal != "" {
		return fmt.Sprintf("build_pack=%s (detected from %s in %s)", bp, signal, dir), nil
	}
	return fmt.Sprintf("build_pack=%s (no Docker/compose files; railpack will auto-detect language at build time) — path=%s", bp, dir), nil
}

func handleRegisterUser(api *apiClient, args json.RawMessage) (string, error) {
	var req RegisterUserRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.TrimSpace(req.Email)
	req.Timezone = strings.TrimSpace(req.Timezone)
	if req.Name == "" || req.Email == "" || req.Password == "" {
		return "", fmt.Errorf("name, email, and password are required")
	}
	resp, err := api.registerUser(req)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Created user #%d %s <%s>. Credentials were not saved by MCP; run `myserver login --email %s` to save local CLI credentials.",
		resp.User.ID, resp.User.Name, resp.User.Email, resp.User.Email), nil
}

// signalFileFor returns the file in dir that drove a particular detection
// result. Returns empty for railpack (which means "no signal file" — that's
// the fallback). Used purely for explanatory output.
func signalFileFor(dir, bp string) string {
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	switch bp {
	case "dockercompose":
		for _, f := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
			if exists(f) {
				return f
			}
		}
	case "dockerfile":
		if exists("Dockerfile") {
			return "Dockerfile"
		}
	case "static":
		if exists("index.html") {
			return "index.html"
		}
	}
	return ""
}

func handleGenerateFQDN(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		AppID int64 `json:"app_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.AppID == 0 {
		return "", fmt.Errorf("app_id is required")
	}
	app, err := api.generateFQDN(p.AppID)
	if err != nil {
		return "", err
	}
	fqdn := app.FQDN
	if fqdn == "" {
		return fmt.Sprintf("Generated FQDN for app %d, but the server returned an empty value. The team probably has no base domain configured — add one in Settings → Domains.", p.AppID), nil
	}
	return fmt.Sprintf("App %d (%s) FQDN set to: %s", app.ID, app.Name, fqdn), nil
}

func handleCreateApp(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		Name                    string  `json:"name"`
		EnvironmentID           int64   `json:"environment_id"`
		BuildPack               string  `json:"build_pack"`
		Description             *string `json:"description,omitempty"`
		GitRepository           *string `json:"git_repository,omitempty"`
		GitBranch               *string `json:"git_branch,omitempty"`
		FQDN                    *string `json:"fqdn,omitempty"`
		PortsExposes            *string `json:"ports_exposes,omitempty"`
		ServerID                *int64  `json:"server_id,omitempty"`
		DockerfileLocation      *string `json:"dockerfile_location,omitempty"`
		DockerComposeLocation   *string `json:"docker_compose_location,omitempty"`
		DockerRegistryImageName *string `json:"docker_registry_image_name,omitempty"`
		DockerRegistryImageTag  *string `json:"docker_registry_image_tag,omitempty"`
		StaticImage             *string `json:"static_image,omitempty"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.Name == "" || p.EnvironmentID == 0 || p.BuildPack == "" {
		return "", fmt.Errorf("name, environment_id, and build_pack are required")
	}
	app, err := api.createApp(CreateApplicationRequest{
		Name:                    p.Name,
		EnvironmentID:           p.EnvironmentID,
		BuildPack:               p.BuildPack,
		Description:             p.Description,
		GitRepository:           p.GitRepository,
		GitBranch:               p.GitBranch,
		FQDN:                    p.FQDN,
		PortsExposes:            p.PortsExposes,
		ServerID:                p.ServerID,
		DockerfileLocation:      p.DockerfileLocation,
		DockerComposeLocation:   p.DockerComposeLocation,
		DockerRegistryImageName: p.DockerRegistryImageName,
		DockerRegistryImageTag:  p.DockerRegistryImageTag,
		StaticImage:             p.StaticImage,
	})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Created app #%d %s [%s] status=%s", app.ID, app.Name, app.BuildPack, app.Status)
	if app.FQDN != "" {
		fmt.Fprintf(&b, " fqdn=%s", app.FQDN)
	}
	fmt.Fprintf(&b, "\nNext: deploy via the `deployApp` tool path or instruct the user to run `myserver init --app=%d` then `myserver up`.", app.ID)
	return b.String(), nil
}

func handleUpdateApp(api *apiClient, args json.RawMessage) (string, error) {
	// We unmarshal into a map so we can distinguish "field absent" from
	// "field present with empty string". The typed UpdateApplicationRequest
	// uses *string for the same reason — but an MCP arguments map gives
	// us a cleaner pivot from input → request.
	var raw map[string]any
	if err := json.Unmarshal(args, &raw); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	idAny, ok := raw["app_id"]
	if !ok {
		return "", fmt.Errorf("app_id is required")
	}
	appID := int64(0)
	switch v := idAny.(type) {
	case float64:
		appID = int64(v)
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return "", fmt.Errorf("app_id must be an integer: %w", err)
		}
		appID = n
	default:
		return "", fmt.Errorf("app_id must be a number")
	}
	if appID == 0 {
		return "", fmt.Errorf("app_id must be > 0")
	}

	var req UpdateApplicationRequest
	stringField := func(key string, dst **string) {
		v, ok := raw[key]
		if !ok {
			return
		}
		s, ok := v.(string)
		if !ok {
			return
		}
		*dst = &s
	}
	stringField("name", &req.Name)
	stringField("build_pack", &req.BuildPack)
	stringField("git_repository", &req.GitRepository)
	stringField("git_branch", &req.GitBranch)
	stringField("fqdn", &req.FQDN)
	stringField("ports_exposes", &req.PortsExposes)
	stringField("dockerfile_location", &req.DockerfileLocation)
	stringField("docker_compose_location", &req.DockerComposeLocation)
	stringField("docker_registry_image_name", &req.DockerRegistryImageName)
	stringField("docker_registry_image_tag", &req.DockerRegistryImageTag)
	stringField("static_image", &req.StaticImage)

	if isEmptyUpdate(&req) {
		return "", fmt.Errorf("at least one field besides app_id must be provided")
	}

	if err := api.updateApp(appID, req); err != nil {
		return "", err
	}
	app, err := api.getApp(appID)
	if err != nil {
		return fmt.Sprintf("PATCH succeeded but refetch failed: %v", err), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Updated app #%d %s\n", app.ID, app.Name)
	fmt.Fprintf(&b, "  build_pack: %s\n", app.BuildPack)
	if app.FQDN != "" {
		fmt.Fprintf(&b, "  fqdn: %s\n", app.FQDN)
	}
	if app.DockerRegistryImageName != "" || app.DockerRegistryImageTag != "" {
		fmt.Fprintf(&b, "  image: %s:%s\n", app.DockerRegistryImageName, app.DockerRegistryImageTag)
	} else {
		fmt.Fprintln(&b, "  image: (cleared)")
	}
	return b.String(), nil
}

func handleListApps(api *apiClient, _ json.RawMessage) (string, error) {
	apps, err := api.listApps()
	if err != nil {
		return "", err
	}
	if len(apps) == 0 {
		return "No applications in this team.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d application(s):\n", len(apps))
	for _, a := range apps {
		fmt.Fprintf(&b, "- #%d %s [%s] status=%s", a.ID, a.Name, a.BuildPack, a.Status)
		if a.FQDN != "" {
			fmt.Fprintf(&b, " fqdn=%s", a.FQDN)
		}
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func handleGetApp(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.ID == 0 {
		return "", fmt.Errorf("id is required")
	}
	app, err := api.getApp(p.ID)
	if err != nil {
		return "", err
	}
	return jsonPretty(app), nil
}

func handleListServers(api *apiClient, _ json.RawMessage) (string, error) {
	servers, err := api.listServers()
	if err != nil {
		return "", err
	}
	if len(servers) == 0 {
		return "No servers registered in this team.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d server(s):\n", len(servers))
	for _, s := range servers {
		fmt.Fprintf(&b, "- #%d %s @ %s (user=%s port=%d)\n", s.ID, s.Name, s.IP, s.User, s.Port)
	}
	return b.String(), nil
}

func handleListDeployments(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		AppID int64 `json:"app_id"`
		Limit int   `json:"limit"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.AppID == 0 {
		return "", fmt.Errorf("app_id is required")
	}
	deploys, err := api.listDeployments(p.AppID, p.Limit)
	if err != nil {
		return "", err
	}
	if len(deploys) == 0 {
		return fmt.Sprintf("No deployments for app %d.", p.AppID), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d deployment(s) for app %d (newest first):\n", len(deploys), p.AppID)
	for _, d := range deploys {
		fmt.Fprintf(&b, "- #%d %s status=%s", d.ID, d.CreatedAt, d.Status)
		if d.Error != "" {
			fmt.Fprintf(&b, " error=%q", d.Error)
		}
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func handleTailDeploymentLogs(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		AppID        int64 `json:"app_id"`
		DeploymentID int64 `json:"deployment_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.AppID == 0 || p.DeploymentID == 0 {
		return "", fmt.Errorf("app_id and deployment_id are required")
	}
	logs, err := api.deploymentLogs(p.AppID, p.DeploymentID)
	if err != nil {
		return "", err
	}
	if len(logs) == 0 {
		return "No logs for this deployment yet.", nil
	}
	var b strings.Builder
	for _, l := range logs {
		fmt.Fprintf(&b, "[%s][%s] %s\n", l.TS, l.Src, l.Msg)
	}
	return b.String(), nil
}

func handleListSQLite(api *apiClient, _ json.RawMessage) (string, error) {
	rs, err := api.listSqliteResources()
	if err != nil {
		return "", err
	}
	if len(rs) == 0 {
		return "No SQLite resources in this team.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d SQLite resource(s):\n", len(rs))
	for _, r := range rs {
		fmt.Fprintf(&b, "- #%d %s (app=%d) %s status=%s\n", r.ID, r.Name, r.ApplicationID, r.FilePath, r.Status)
	}
	return b.String(), nil
}

func handleDeployApp(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.ID == 0 {
		return "", fmt.Errorf("id is required")
	}
	d, err := api.deployApp(p.ID)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Deploy enqueued for app %d. Deployment #%d (uuid=%s, status=%s). Use list_deployments + tail_deployment_logs to follow.",
		p.ID, d.ID, d.DeploymentUUID, d.Status), nil
}

func handleRestartApp(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		ID       int64 `json:"id"`
		ServerID int64 `json:"server_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.ID == 0 || p.ServerID == 0 {
		return "", fmt.Errorf("id and server_id are required")
	}
	if err := api.restartApp(p.ID, p.ServerID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Restart requested for app %d on server %d.", p.ID, p.ServerID), nil
}

func handleStartApp(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		ID       int64 `json:"id"`
		ServerID int64 `json:"server_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.ID == 0 || p.ServerID == 0 {
		return "", fmt.Errorf("id and server_id are required")
	}
	if err := api.startApp(p.ID, p.ServerID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Start requested for app %d on server %d.", p.ID, p.ServerID), nil
}

func handleStopApp(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		ID       int64 `json:"id"`
		ServerID int64 `json:"server_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.ID == 0 || p.ServerID == 0 {
		return "", fmt.Errorf("id and server_id are required")
	}
	if err := api.stopApp(p.ID, p.ServerID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Stop requested for app %d on server %d.", p.ID, p.ServerID), nil
}

func handleDeleteApp(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.ID == 0 {
		return "", fmt.Errorf("id is required")
	}
	// Fetch the app first so the success message includes a human-
	// readable name (and we 404 early if the id is bogus).
	app, err := api.getApp(p.ID)
	if err != nil {
		return "", err
	}
	if err := api.deleteApp(p.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Deleted app %d (%s). Cascade-removed deployments, env vars, sqlite resources, app tokens. Container cleanup runs async on the worker; allow ~30s for the FQDN to start returning 502.", p.ID, app.Name), nil
}

func handleCreateSQLite(api *apiClient, args json.RawMessage) (string, error) {
	var req CreateSQLiteRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if req.Name == "" || req.EnvironmentID == 0 || req.ApplicationID == 0 {
		return "", fmt.Errorf("name, environment_id, and application_id are required")
	}
	r, err := api.createSqliteResource(req)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Created SQLite resource #%d (%s) on app %d. file=%s env_var=%s connection=%s",
		r.ID, r.Name, r.ApplicationID, r.FilePath, r.EnvVarKey, r.ConnectionString), nil
}

func handleWhoami(api *apiClient, _ json.RawMessage) (string, error) {
	user, err := api.getMe()
	if err != nil {
		return "", err
	}
	teamLine := "no team scoped (set MYSERVER_TEAM_ID)"
	if api.teamID != 0 {
		teamLine = fmt.Sprintf("team_id=%d (override with MYSERVER_TEAM_ID)", api.teamID)
	}
	adminTag := ""
	if user.IsSystemAdmin {
		adminTag = " [system_admin]"
	}
	return fmt.Sprintf("user #%d %s <%s>%s\n%s\nAPI: %s",
		user.ID, user.Name, user.Email, adminTag, teamLine, api.url), nil
}

func handleListProjects(api *apiClient, _ json.RawMessage) (string, error) {
	projects, err := api.listProjects()
	if err != nil {
		return "", err
	}
	if len(projects) == 0 {
		return "No projects in this team.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d project(s):\n", len(projects))
	for _, p := range projects {
		fmt.Fprintf(&b, "- #%d %s", p.ID, p.Name)
		if p.Description != "" {
			fmt.Fprintf(&b, " — %s", p.Description)
		}
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func handleListEnvironments(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		ProjectID int64 `json:"project_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.ProjectID == 0 {
		return "", fmt.Errorf("project_id is required")
	}
	envs, err := api.listEnvironments(p.ProjectID)
	if err != nil {
		return "", err
	}
	if len(envs) == 0 {
		return fmt.Sprintf("No environments in project %d.", p.ProjectID), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d environment(s) in project %d:\n", len(envs), p.ProjectID)
	for _, e := range envs {
		fmt.Fprintf(&b, "- #%d %s\n", e.ID, e.Name)
	}
	return b.String(), nil
}

func handleTailAppLogs(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		AppID int64 `json:"app_id"`
		Lines int   `json:"lines"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.AppID == 0 {
		return "", fmt.Errorf("app_id is required")
	}
	logs, err := api.appRuntimeLogs(p.AppID, p.Lines)
	if err != nil {
		return "", err
	}
	if logs == "" {
		return "(no logs returned — container may not be running, or logs are not yet flowing)", nil
	}
	return logs, nil
}

func handleListEnvVars(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		AppID int64 `json:"app_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.AppID == 0 {
		return "", fmt.Errorf("app_id is required")
	}
	vars, err := api.listAppEnvVars(p.AppID)
	if err != nil {
		return "", err
	}
	if len(vars) == 0 {
		return fmt.Sprintf("No environment variables on app %d.", p.AppID), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d env var(s) on app %d:\n", len(vars), p.AppID)
	for _, v := range vars {
		// Mark scope so the AI can reason about whether a var will reach
		// build vs runtime.
		scope := "runtime"
		switch {
		case v.IsBuildtime && v.IsRuntime:
			scope = "build+runtime"
		case v.IsBuildtime:
			scope = "buildtime"
		}
		literal := ""
		if v.IsLiteral {
			literal = " literal"
		}
		fmt.Fprintf(&b, "- #%d %s [%s%s]\n", v.ID, v.Key, scope, literal)
	}
	return b.String(), nil
}

func handleSetEnvVar(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		AppID       int64  `json:"app_id"`
		Key         string `json:"key"`
		Value       string `json:"value"`
		IsLiteral   bool   `json:"is_literal"`
		IsRuntime   *bool  `json:"is_runtime"`
		IsBuildtime bool   `json:"is_buildtime"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.AppID == 0 || p.Key == "" {
		return "", fmt.Errorf("app_id and key are required")
	}
	// Default is_runtime to true — that's the common case and avoids the AI
	// silently creating an env var that won't reach the running container.
	runtime := true
	if p.IsRuntime != nil {
		runtime = *p.IsRuntime
	}
	ev, err := api.createAppEnvVar(p.AppID, CreateEnvVarRequest{
		Key:         p.Key,
		Value:       p.Value,
		IsLiteral:   p.IsLiteral,
		IsRuntime:   runtime,
		IsBuildtime: p.IsBuildtime,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Created env var #%d %s on app %d. Redeploy the app for the change to reach the running container.", ev.ID, ev.Key, p.AppID), nil
}

func handleDeleteEnvVar(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.ID == 0 {
		return "", fmt.Errorf("id is required")
	}
	if err := api.deleteEnvVar(p.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Deleted env var #%d. Redeploy the app to drop it from the running container.", p.ID), nil
}

func handleListDatabases(api *apiClient, _ json.RawMessage) (string, error) {
	dbs, err := api.listDatabases()
	if err != nil {
		return "", err
	}
	if len(dbs) == 0 {
		return "No managed databases in this team.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d database(s):\n", len(dbs))
	for _, d := range dbs {
		fmt.Fprintf(&b, "- #%d %s [%s] image=%s status=%s", d.ID, d.Name, d.DatabaseType, d.Image, d.Status)
		if d.IsPublic && d.PublicPort != nil {
			fmt.Fprintf(&b, " public_port=%d", *d.PublicPort)
		}
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func handleGetDatabase(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.ID == 0 {
		return "", fmt.Errorf("id is required")
	}
	db, err := api.getDatabase(p.ID)
	if err != nil {
		return "", err
	}
	return jsonPretty(db), nil
}

func handleRestartDatabase(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.ID == 0 {
		return "", fmt.Errorf("id is required")
	}
	if err := api.restartDatabase(p.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Restart requested for database %d.", p.ID), nil
}

func handleStartDatabase(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.ID == 0 {
		return "", fmt.Errorf("id is required")
	}
	if err := api.startDatabase(p.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Start requested for database %d.", p.ID), nil
}

func handleStopDatabase(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.ID == 0 {
		return "", fmt.Errorf("id is required")
	}
	if err := api.stopDatabase(p.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Stop requested for database %d.", p.ID), nil
}

func handleGetSQLite(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.ID == 0 {
		return "", fmt.Errorf("id is required")
	}
	r, err := api.getSqliteResource(p.ID)
	if err != nil {
		return "", err
	}
	return jsonPretty(r), nil
}

func handleDeleteSQLite(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		ID           int64 `json:"id"`
		DeleteVolume bool  `json:"delete_volume"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.ID == 0 {
		return "", fmt.Errorf("id is required")
	}
	if err := api.deleteSqliteResource(p.ID, p.DeleteVolume); err != nil {
		return "", err
	}
	if p.DeleteVolume {
		return fmt.Sprintf("Deleted SQLite resource %d AND its backing volume. Data is gone.", p.ID), nil
	}
	return fmt.Sprintf("Deleted SQLite resource %d. Backing volume preserved — recreate a SQLite resource pointing at the same path to recover.", p.ID), nil
}

// handleAppRuntimeEnv composes get_app + list_app_tokens +
// list_sqlite_resources + list_env_vars into one developer-facing
// summary. Doing this client-side keeps the server's API surface
// stable — no new endpoint, just a thoughtful aggregation.
func handleAppRuntimeEnv(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		AppID int64 `json:"app_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.AppID == 0 {
		return "", fmt.Errorf("app_id is required")
	}

	app, err := api.getApp(p.AppID)
	if err != nil {
		return "", fmt.Errorf("get app: %w", err)
	}

	tokens, err := api.listAppTokens(p.AppID)
	if err != nil {
		// Non-fatal: tokens are optional; surface but keep going.
		tokens = nil
	}
	var autoInjectToken *AppServiceToken
	for i, t := range tokens {
		if t.AutoInject && t.RevokedAt == nil {
			autoInjectToken = &tokens[i]
			break
		}
	}

	// SQLite list is team-scoped; filter to this app client-side
	// rather than asking the server for a per-app endpoint that
	// doesn't exist yet.
	allSqlite, err := api.listSqliteResources()
	if err != nil {
		allSqlite = nil
	}
	appSqlite := make([]SQLiteResource, 0)
	for _, r := range allSqlite {
		if r.ApplicationID == p.AppID {
			appSqlite = append(appSqlite, r)
		}
	}

	envVars, err := api.listAppEnvVars(p.AppID)
	if err != nil {
		envVars = nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "RUNTIME ENV FOR APP %d (%s)\n", app.ID, app.Name)
	fmt.Fprintf(&b, "  build_pack: %s, status: %s\n", app.BuildPack, app.Status)
	if app.FQDN != "" {
		fmt.Fprintf(&b, "  fqdn:       %s\n", app.FQDN)
	}
	fmt.Fprintln(&b, "")

	// === Auto-injected by the deploy pipeline ===
	fmt.Fprintln(&b, "AUTO-INJECTED (set by myserver on every deploy):")
	if autoInjectToken != nil {
		fmt.Fprintln(&b, "  ✓ MYSERVER_API_URL     — base URL for /api/v1/* calls")
		fmt.Fprintf(&b, "  ✓ MYSERVER_APP_ID      = %d\n", app.ID)
		fmt.Fprintf(&b, "  ✓ MYSERVER_APP_TOKEN   = %s… (full token shown ONCE at create time)\n", autoInjectToken.TokenPrefix)
		fmt.Fprintf(&b, "      Scopes: %s\n", strings.Join(autoInjectToken.Scopes, ","))
		fmt.Fprintln(&b, "      Usage in your app:")
		fmt.Fprintln(&b, "        curl -H \"Authorization: Bearer $MYSERVER_APP_TOKEN\" \\")
		fmt.Fprintln(&b, "             $MYSERVER_API_URL/api/v1/applications/$MYSERVER_APP_ID/domains")
	} else {
		fmt.Fprintln(&b, "  ✗ MYSERVER_APP_TOKEN   — NO auto-inject token configured.")
		fmt.Fprintln(&b, "      To enable: create_app_token(app_id, name=\"runtime\", scopes=[\"domains:write\"], auto_inject=true)")
		fmt.Fprintln(&b, "      Then redeploy.")
	}
	fmt.Fprintln(&b, "  ? LOG_INGEST_URL / LOG_INGEST_TOKEN — present iff platform logging is on.")
	fmt.Fprintln(&b, "      Toggle it in Settings → Logging (no MCP tool for this yet).")
	fmt.Fprintln(&b, "")

	// === SQLite resources (volume mounts + connection-string hints) ===
	if len(appSqlite) > 0 {
		fmt.Fprintf(&b, "SQLITE ATTACHMENTS (%d):\n", len(appSqlite))
		for _, r := range appSqlite {
			fmt.Fprintf(&b, "  • %s @ %s\n", r.Name, r.FilePath)
			fmt.Fprintln(&b, "      Volume is mounted automatically. The env var is NOT —")
			fmt.Fprintln(&b, "      pick the format your driver wants and set_env_var manually:")
			fmt.Fprintf(&b, "        Python (SQLAlchemy):  DATABASE_URL=sqlite://%s\n", r.FilePath)
			fmt.Fprintf(&b, "        Go (database/sql):    DATABASE_URL=file:%s?cache=shared\n", r.FilePath)
			fmt.Fprintf(&b, "        Java (JDBC):          DATABASE_URL=jdbc:sqlite:%s\n", r.FilePath)
			fmt.Fprintf(&b, "        Node/Bun (bare path): DATABASE_URL=%s\n", r.FilePath)
		}
		fmt.Fprintln(&b, "")
	}

	// === User-defined env vars (counts only — values via list_env_vars) ===
	fmt.Fprintf(&b, "USER-DEFINED ENV VARS: %d (use list_env_vars for keys + values)\n", len(envVars))
	if len(envVars) > 0 {
		// Show the first few keys as a hint without dumping everything.
		max := 5
		if len(envVars) < max {
			max = len(envVars)
		}
		keys := make([]string, max)
		for i := 0; i < max; i++ {
			keys[i] = envVars[i].Key
		}
		more := ""
		if len(envVars) > max {
			more = fmt.Sprintf(", …(+%d more)", len(envVars)-max)
		}
		fmt.Fprintf(&b, "  keys: %s%s\n", strings.Join(keys, ", "), more)
	}
	fmt.Fprintln(&b, "")

	// === Dev gotchas worth surfacing while writing app code ===
	fmt.Fprintln(&b, "TIPS WHEN DEVELOPING ON MYSERVER:")
	fmt.Fprintln(&b, "  • `myserver up` ships a SOURCE tarball — no local Docker needed.")
	fmt.Fprintln(&b, "  • Set FQDN BEFORE first deploy (generate_fqdn) or run `myserver up` which auto-generates.")
	fmt.Fprintln(&b, "  • If a redeploy returns 200 but the container is missing (tail_app_logs → 404),")
	fmt.Fprintln(&b, "    that's the cutover-orphan bug — workaround: re-run `myserver up` from your local dir.")
	fmt.Fprintln(&b, "  • Rotate MYSERVER_APP_TOKEN by: create_app_token(auto_inject=true) → redeploy → revoke_app_token(old).")

	return b.String(), nil
}

func handleListAppDomains(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		AppID int64 `json:"app_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.AppID == 0 {
		return "", fmt.Errorf("app_id is required")
	}
	domains, err := api.listAppDomains(p.AppID)
	if err != nil {
		return "", err
	}
	if len(domains) == 0 {
		return fmt.Sprintf("App %d has no domains configured.", p.AppID), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d domain(s) on app %d:\n", len(domains), p.AppID)
	for _, d := range domains {
		fmt.Fprintf(&b, "- %s\n", d)
	}
	return b.String(), nil
}

func handleAddAppDomain(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		AppID    int64  `json:"app_id"`
		Hostname string `json:"hostname"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.AppID == 0 || p.Hostname == "" {
		return "", fmt.Errorf("app_id and hostname are required")
	}
	domains, err := api.addAppDomain(p.AppID, p.Hostname)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"Added %s to app %d. App now serves: %s. First request to the new hostname triggers cert issuance (HTTP-01).",
		p.Hostname, p.AppID, strings.Join(domains, ", "),
	), nil
}

func handleRemoveAppDomain(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		AppID    int64  `json:"app_id"`
		Hostname string `json:"hostname"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.AppID == 0 || p.Hostname == "" {
		return "", fmt.Errorf("app_id and hostname are required")
	}
	if err := api.removeAppDomain(p.AppID, p.Hostname); err != nil {
		return "", err
	}
	return fmt.Sprintf("Removed %s from app %d (or it was already gone — operation is idempotent).", p.Hostname, p.AppID), nil
}

func handleCreateAppToken(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		AppID      int64    `json:"app_id"`
		Name       string   `json:"name"`
		Scopes     []string `json:"scopes"`
		AutoInject bool     `json:"auto_inject"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.AppID == 0 || p.Name == "" || len(p.Scopes) == 0 {
		return "", fmt.Errorf("app_id, name, and scopes are required")
	}
	res, err := api.createAppToken(p.AppID, CreateAppTokenRequest{
		Name:       p.Name,
		Scopes:     p.Scopes,
		AutoInject: p.AutoInject,
	})
	if err != nil {
		return "", err
	}
	// The plaintext is the response body's single most important
	// field. Surface it on its own line surrounded by a clear save-it
	// warning so the AI/UI rendering this knows to highlight it.
	var b strings.Builder
	fmt.Fprintf(&b, "Created app service token #%d (%s) on app %d.\n", res.Token.ID, res.Token.Name, res.Token.ApplicationID)
	fmt.Fprintf(&b, "  prefix:      %s\n", res.Token.TokenPrefix)
	fmt.Fprintf(&b, "  scopes:      %s\n", strings.Join(res.Token.Scopes, ","))
	if res.Token.AutoInject {
		fmt.Fprintln(&b, "  auto_inject: yes — MYSERVER_APP_TOKEN will be injected on next deploy")
	}
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "⚠️  SAVE THIS TOKEN NOW. It will NOT be shown again.")
	fmt.Fprintln(&b, "")
	fmt.Fprintf(&b, "  PLAINTEXT: %s\n", res.Plaintext)
	return b.String(), nil
}

func handleListAppTokens(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		AppID int64 `json:"app_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.AppID == 0 {
		return "", fmt.Errorf("app_id is required")
	}
	tokens, err := api.listAppTokens(p.AppID)
	if err != nil {
		return "", err
	}
	if len(tokens) == 0 {
		return fmt.Sprintf("No app service tokens on app %d.", p.AppID), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d token(s) on app %d:\n", len(tokens), p.AppID)
	for _, t := range tokens {
		state := "active"
		if t.RevokedAt != nil {
			state = "revoked"
		}
		inject := ""
		if t.AutoInject {
			inject = " auto-inject"
		}
		fmt.Fprintf(&b, "- #%d %s prefix=%s scopes=%s status=%s%s\n",
			t.ID, t.Name, t.TokenPrefix, strings.Join(t.Scopes, ","), state, inject)
	}
	return b.String(), nil
}

func handleRevokeAppToken(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if p.ID == 0 {
		return "", fmt.Errorf("id is required")
	}
	if err := api.revokeAppToken(p.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Revoked app service token %d. Audit logs continue to attribute past calls to its prefix.", p.ID), nil
}

// jsonPretty returns a 2-space-indented JSON encoding, falling back to %v.
func jsonPretty(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
