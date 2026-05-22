#!/usr/bin/env node
//
// Node shim that locates the matching prebuilt Go binary from the
// platform-specific @serverops/myserver-cli-<os>-<arch> package and
// execs it with the user's argv. Inspired by the esbuild + biome
// approach: each platform binary is its own tiny npm package listed
// as an optionalDependency, so `npm install` only pulls the one your
// host actually needs and there's no postinstall download step.

const { spawnSync } = require("node:child_process");
const path = require("node:path");

function pickPackage() {
  const platform = process.platform; // 'darwin' | 'linux' | 'win32' | …
  const arch = process.arch;         // 'x64' | 'arm64' | …
  const map = {
    "darwin-arm64": "@serverops/myserver-cli-darwin-arm64",
    "darwin-x64": "@serverops/myserver-cli-darwin-x64",
    "linux-arm64": "@serverops/myserver-cli-linux-arm64",
    "linux-x64": "@serverops/myserver-cli-linux-x64",
    "win32-x64": "@serverops/myserver-cli-win32-x64",
  };
  const key = `${platform}-${arch}`;
  const pkg = map[key];
  if (!pkg) {
    console.error(
      `myserver-cli: unsupported platform '${key}'. ` +
        "Supported: " + Object.keys(map).join(", ") + ".\n" +
        "Download a binary directly from https://github.com/Nervara/myserver-cli/releases"
    );
    process.exit(1);
  }
  return pkg;
}

function resolveBinary(pkg) {
  const binName = process.platform === "win32" ? "myserver.exe" : "myserver";
  try {
    return require.resolve(`${pkg}/bin/${binName}`);
  } catch (e) {
    console.error(
      `myserver-cli: platform package ${pkg} was not installed.\n` +
        "npm probably skipped it during install. Try:\n" +
        "  npm install --include=optional --force " + pkg
    );
    process.exit(1);
  }
}

const pkg = pickPackage();
const binary = resolveBinary(pkg);
const result = spawnSync(binary, process.argv.slice(2), {
  stdio: "inherit",
  shell: false,
});
if (result.error) {
  console.error(`myserver-cli: failed to exec ${binary}: ${result.error.message}`);
  process.exit(1);
}
process.exit(result.status ?? 1);
