#!/usr/bin/env node
//
// Runtime shim — execs the prebuilt Go binary that was downloaded by the
// postinstall script. If the binary is missing (postinstall skipped or
// failed), prints a helpful error so the user can fix it.

const { spawnSync } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");

function resolveBinary() {
  const binName = process.platform === "win32" ? "myserver.exe" : "myserver";
  const binPath = path.join(__dirname, binName);
  if (fs.existsSync(binPath)) return binPath;

  console.error(
    `myserver-cli: prebuilt binary not found at ${binPath}.\n\n` +
      "The binary should have been downloaded during install. This can happen if:\n" +
      "  - The postinstall script was skipped (try reinstalling)\n" +
      "  - Your platform isn't supported (supported: darwin-arm64, darwin-x64,\n" +
      "    linux-arm64, linux-x64, win32-arm64, win32-x64)\n" +
      "  - You're in an offline environment\n\n" +
      "Fixes:\n" +
      "  npm install --ignore-scripts=false @serverops/myserver-cli\n" +
      "  # or download directly:\n" +
      "  # https://github.com/Nervara/myserver-cli/releases\n" +
      "\n" +
      "Reach out at https://github.com/Nervara/myserver-cli/issues if this persists.",
  );
  process.exit(1);
}

const binary = resolveBinary();
const result = spawnSync(binary, process.argv.slice(2), {
  stdio: "inherit",
  shell: false,
});
if (result.error) {
  console.error(`myserver-cli: failed to exec ${binary}: ${result.error.message}`);
  process.exit(1);
}
process.exit(result.status ?? 1);
