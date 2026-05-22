// myserver-cli — Railway-style local→prod deploy for myserver-hosted apps.
//
// Usage:
//
//	myserver login                  # prompt, store token
//	myserver init                   # interactive: pick team/server/app, write myserver.json
//	myserver up                     # rsync source, build remotely, push, deploy
//	myserver logs --follow          # tail the latest deployment
//	myserver status                 # current app + last 5 deploys
//
// Stays minimal-deps: stdlib only, no cobra/urfave. ~600 LoC across this dir.
package main

import (
	"fmt"
	"os"
)

const (
	cliVersion      = "0.1.0"
	credentialsPath = ".myserver/credentials.json" // under $HOME
	projectConfigFn = "myserver.json"              // in cwd
)

type cmd struct {
	name string
	help string
	run  func(args []string) error
}

var commands = []cmd{
	{"login", "authenticate against a myserver instance", runLogin},
	{"logout", "forget stored credentials", runLogout},
	{"whoami", "show the currently logged-in user", runWhoami},
	{"init", "wire the current directory to an app", runInit},
	{"project", "list / create projects (`project list`, `project create`)", runProject},
	{"env", "list / create environments (`env list`, `env create`)", runEnv},
	{"app", "create / manage applications (`app create`)", runApp},
	{"sqlite", "attach / list managed SQLite resources (`sqlite create`)", runSqlite},
	{"token", "manage app service tokens (`token create/list/revoke`)", runToken},
	{"domains", "manage app hostnames (`domains list/add/remove`)", runDomains},
	{"env-vars", "manage app env vars (`env-vars list/set/delete`)", runEnvVars},
	{"db", "manage managed databases (`db list/start/stop/restart/delete`)", runDB},
	{"backup", "system backups (`backup list/create/download`) — admin only", runBackup},
	{"server", "register / list / delete destination servers (`server register`)", runServerCmd},
	{"up", "deploy the current directory", runUp},
	{"logs", "stream logs from the latest deployment", runLogs},
	{"status", "show the bound app + recent deploy history", runStatus},
	{"mcp", "MCP server (stdio) — `mcp install` wires it into your AI editor", runMCP},
	{"version", "print version", runVersion},
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	name := os.Args[1]
	for _, c := range commands {
		if c.name == name {
			if err := c.run(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "myserver: %v\n", err)
				os.Exit(1)
			}
			return
		}
	}
	switch name {
	case "-h", "--help", "help":
		usage()
		return
	}
	fmt.Fprintf(os.Stderr, "myserver: unknown command %q\n", name)
	usage()
	os.Exit(2)
}

func usage() {
	fmt.Fprintf(os.Stderr, "myserver — local→prod deploy CLI\n\nUsage:\n  myserver <command> [flags]\n\nCommands:\n")
	for _, c := range commands {
		fmt.Fprintf(os.Stderr, "  %-9s  %s\n", c.name, c.help)
	}
	fmt.Fprintf(os.Stderr, "\nRun `myserver <command> -h` for command-specific flags.\n")
}

func runVersion(_ []string) error {
	fmt.Println("myserver-cli", cliVersion)
	return nil
}
