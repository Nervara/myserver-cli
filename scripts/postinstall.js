#!/usr/bin/env node
//
// postinstall — download the right Go binary for this platform from
// GitHub releases and drop it into bin/.
//
// Design:
//   - Detects OS + arch at install time
//   - Downloads `myserver-<os>-<arch>[.exe]` + `.sha256` from
//     $MYSERVER_CLI_DOWNLOAD_BASE (defaults to GitHub releases)
//   - Defaults to the package version as the release tag (vX.Y.Z)
//   - Override with:
//       MYSERVER_CLI_DOWNLOAD_BASE  → base URL (e.g. http://localhost:8080)
//       MYSERVER_CLI_VERSION        → release tag (e.g. v0.2.0)
//   - Verifies sha256 (skips silently if .sha256 is not reachable —
//     old releases or custom mirrors may not have it)
//   - No native dependencies — stdlib only
//
// Failures are non-fatal: the runtime shim (bin/myserver.js) prints a
// helpful error when the binary is missing, so a broken postinstall
// doesn't block `npm install` from succeeding.

const https = require("node:https");
const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");
const { pipeline } = require("node:stream");
const { promisify } = require("node:util");
const streamPipeline = promisify(pipeline);

// ── Resolve platform ──────────────────────────────────────────────────

function platformKey() {
  const p = process.platform; // 'darwin' | 'linux' | 'win32' | …
  const a = process.arch;     // 'x64' | 'arm64' | …
  const map = {
    "darwin-arm64": { os: "darwin", arch: "arm64" },
    "darwin-x64":   { os: "darwin", arch: "amd64" },
    "linux-arm64":  { os: "linux",  arch: "arm64" },
    "linux-x64":    { os: "linux",  arch: "amd64" },
    "win32-x64":    { os: "windows", arch: "amd64" },
    "win32-arm64":  { os: "windows", arch: "arm64" },
  };
  const key = `${p}-${a}`;
  const m = map[key];
  if (!m) return null;
  const ext = m.os === "windows" ? ".exe" : "";
  return {
    os: m.os,
    arch: m.arch,
    filename: `myserver-${m.os}-${m.arch}${ext}`,
    binName: `myserver${ext}`,
    key,
  };
}

// ── Config ────────────────────────────────────────────────────────────

const pkg = JSON.parse(
  fs.readFileSync(path.join(__dirname, "..", "package.json"), "utf8"),
);
const version = process.env.MYSERVER_CLI_VERSION || `v${pkg.version}`;
const downloadBase =
  (process.env.MYSERVER_CLI_DOWNLOAD_BASE || "")
    .replace(/\/+$/, "") ||
  `https://github.com/Nervara/myserver-cli/releases/download/${version}`;

// ── Helpers ───────────────────────────────────────────────────────────

function fetch(url) {
  return new Promise((resolve, reject) => {
    https
      .get(url, { headers: { "User-Agent": "myserver-cli-installer" } }, (res) => {
        if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
          // Follow one redirect (GitHub release downloads redirect to objects)
          https
            .get(res.headers.location, { headers: { "User-Agent": "myserver-cli-installer" } }, (r2) => {
              if (r2.statusCode !== 200) {
                reject(new Error(`HTTP ${r2.statusCode} for ${url}`));
                return;
              }
              resolve(r2);
            })
            .on("error", reject);
          return;
        }
        if (res.statusCode !== 200) {
          reject(new Error(`HTTP ${res.statusCode} for ${url}`));
          return;
        }
        resolve(res);
      })
      .on("error", reject);
  });
}

async function downloadFile(url, dest) {
  const res = await fetch(url);
  await fs.promises.mkdir(path.dirname(dest), { recursive: true });
  const tmp = dest + ".tmp";
  await streamPipeline(res, fs.createWriteStream(tmp));
  fs.renameSync(tmp, dest);
  await fs.promises.chmod(dest, 0o755);
}

async function downloadText(url) {
  const res = await fetch(url);
  const chunks = [];
  for await (const chunk of res) chunks.push(chunk);
  return Buffer.concat(chunks).toString("utf8").trim();
}

function sha256File(p) {
  return new Promise((resolve, reject) => {
    const hash = crypto.createHash("sha256");
    const stream = fs.createReadStream(p);
    stream.on("data", (d) => hash.update(d));
    stream.on("end", () => resolve(hash.digest("hex")));
    stream.on("error", reject);
  });
}

function log(msg) {
  const prefix = "\x1b[36m[myserver-cli]\x1b[0m";
  console.error(`${prefix} ${msg}`);
}

// ── Main ──────────────────────────────────────────────────────────────

async function main() {
  const plat = platformKey();
  if (!plat) {
    log(
      `unsupported platform '${process.platform}-${process.arch}' — ` +
        "skipping binary download. Use the system package or download from\n" +
        `  ${downloadBase}\n` +
        "The runtime shim will fall back to a descriptive error message.",
    );
    return;
  }

  const binDir = path.join(__dirname, "..", "bin");
  const binPath = path.join(binDir, plat.binName);

  // Skip if binary already present (reinstall / offline scenario).
  if (fs.existsSync(binPath)) {
    log(`${plat.binName} already present, skipping download.`);
    return;
  }

  const binaryUrl = `${downloadBase}/${plat.filename}`;
  const shaUrl = `${binaryUrl}.sha256`;

  log(`downloading ${plat.filename} for ${plat.key} …`);

  try {
    await downloadFile(binaryUrl, binPath);
  } catch (err) {
    log(`download failed (${err.message}) — binary will not be available.`);
    log(`expected at: ${binaryUrl}`);
    log("install manually: npm install -g @serverops/myserver-cli may work");
    return;
  }

  // Verify sha256 when available. Silently skip on failure — mirrors
  // install-cli.sh behaviour for old / custom releases.
  try {
    const expected = (await downloadText(shaUrl)).split(/\s+/)[0];
    const actual = await sha256File(binPath);
    if (expected.length === 64 && actual !== expected) {
      log(`checksum mismatch — removing downloaded binary.`);
      log(`  expected: ${expected}`);
      log(`  actual:   ${actual}`);
      fs.unlinkSync(binPath);
      return;
    }
    if (expected.length === 64) {
      log(`checksum verified (${expected.slice(0, 12)}…)`);
    }
  } catch (_) {
    // No sha256 available — proceed anyway.
  }

  log(`installed ${plat.binName} (${version})`);
}

main().catch((err) => {
  log(`unexpected error: ${err.message}`);
});
