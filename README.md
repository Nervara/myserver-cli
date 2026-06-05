# myserver-cli

The official command-line interface and MCP server for [myserver](https://serverops.cloud) — a self-hostable PaaS for deploying apps, databases, and services.

Built as a single Go binary. No daemon, no background process. Wraps the myserver HTTP API. Distributed as a single npm package that downloads the right binary for your platform at install time.

## Install

```bash
npm install -g @serverops/myserver-cli
# or with bun
bun add -g @serverops/myserver-cli
```

The postinstall script detects your OS + arch and downloads the matching prebuilt binary from GitHub releases. No native dependencies, no per-platform packages.

For global installs, the installer can also wire the MCP server into supported AI editors. If npm is running interactively, it asks whether to run `myserver mcp install`; otherwise it prints the command to run later.

**Env overrides** (for testing / custom mirrors):

```bash
# Use a local mirror
MYSERVER_CLI_DOWNLOAD_BASE=http://localhost:8080 npm install -g @serverops/myserver-cli

# Pin a specific version tag
MYSERVER_CLI_VERSION=v0.2.0 npm install -g @serverops/myserver-cli

# Install MCP integration without prompting
MYSERVER_CLI_INSTALL_MCP=1 npm install -g @serverops/myserver-cli --foreground-scripts

# Skip MCP integration explicitly
MYSERVER_CLI_INSTALL_MCP=0 npm install -g @serverops/myserver-cli
```

Standalone binary downloads are attached to every [GitHub release](https://github.com/Nervara/myserver-cli/releases) for `darwin-arm64`, `darwin-amd64`, `linux-arm64`, `linux-amd64`, `windows-arm64`, and `windows-amd64`.

## Quick start

```bash
# Sign in (interactive, stores token in ~/.myserver/credentials.json)
myserver login

# Wire the current directory to an app (interactive: pick team / server / app)
myserver init

# Build remotely and deploy
myserver up

# Tail the latest deployment
myserver logs --follow

# Status
myserver status
```

Override the API endpoint when self-hosting:

```bash
myserver login --api https://myserver.your-domain.com
```

## Commands

```
login       authenticate against a myserver instance
auth        authentication helpers (`auth register`)
logout      forget stored credentials
whoami      show the currently logged-in user
init        wire the current directory to an app
project     list / create projects
env         list / create environments
app         create / manage applications
sqlite      attach / list managed SQLite resources
token       manage app service tokens
domains     manage app hostnames
env-vars    manage app env vars
db          manage managed databases
backup      system backups (admin only)
server      register / list / delete destination servers
up          deploy the current directory
logs        stream logs from the latest deployment
status      show the bound app + recent deploy history
mcp         MCP server (stdio) — wire into your AI editor
version     print version
```

`myserver <command> -h` for command-specific flags.

## MCP server (Claude Desktop, Cursor, Cline, etc.)

The CLI ships a [Model Context Protocol](https://modelcontextprotocol.io/) server over stdio so any MCP-capable AI client can drive myserver.

```bash
# One-shot install for popular editors
myserver mcp install

# Validate the published MCP tool catalog
myserver mcp doctor

# Or run directly (the editor spawns this on stdin/stdout)
myserver mcp
```

Tools exposed include: `list_apps`, `create_app`, `deploy_app`, `tail_app_logs`, `list_databases`, `set_env_var`, plus a `whoami` and `generate_fqdn` for sanity checks.

## Self-hosting

This CLI targets the public SaaS at `app.serverops.cloud` by default. To run against your own myserver instance:

```bash
myserver login --api https://your-myserver-instance.example.com
```

This CLI is open-source under MIT. The myserver server is sold as a managed SaaS; self-hosting plans are tracked separately — check [serverops.cloud](https://serverops.cloud) for current options.

## Contributing

Pull requests welcome. The CLI is stdlib-only (no cobra/urfave/etc) — the dependency surface is intentionally minimal.

```bash
go build ./cmd/myserver
go test ./...
```

## License

MIT — see [LICENSE](LICENSE).
