const assert = require("node:assert/strict");
const test = require("node:test");

const {
  formatMCPSkippedMessage,
  shouldOfferMCPInstall,
  parseMCPInstallChoice,
} = require("./postinstall");

test("shouldOfferMCPInstall only prompts during interactive global installs", () => {
  assert.equal(
    shouldOfferMCPInstall({
      env: { npm_config_global: "true" },
      stdinTTY: true,
      stdoutTTY: true,
    }),
    true,
  );

  assert.equal(
    shouldOfferMCPInstall({
      env: { npm_config_global: "true", CI: "true" },
      stdinTTY: true,
      stdoutTTY: true,
    }),
    false,
  );

  assert.equal(
    shouldOfferMCPInstall({
      env: {},
      stdinTTY: true,
      stdoutTTY: true,
    }),
    false,
  );
});

test("parseMCPInstallChoice defaults to no unless the answer is yes", () => {
  assert.equal(parseMCPInstallChoice("y"), true);
  assert.equal(parseMCPInstallChoice("YES\n"), true);
  assert.equal(parseMCPInstallChoice(""), false);
  assert.equal(parseMCPInstallChoice("n"), false);
});

test("formatMCPSkippedMessage gives the user a clear next command", () => {
  const message = formatMCPSkippedMessage("non-interactive npm install");

  assert.match(message, /MCP integration was not installed/);
  assert.match(message, /myserver mcp install/);
  assert.match(message, /"args": \["mcp"\]/);
  assert.match(message, /non-interactive npm install/);
});
