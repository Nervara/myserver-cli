// Generate per-platform npm packages at release time.
//
// Inputs:  dist/artifacts.json — written by Goreleaser, lists every
//          artifact with its exact path + goos + goarch + type.
// Outputs: npm-packages/<os>-<arch>/{package.json, bin/myserver(.exe),
//          README.md, LICENSE}
//
// Each per-platform package is tiny (just the binary + a 4-line
// package.json with `os` + `cpu` constraints so npm only picks it up
// on a matching host) and is published independently. The top-level
// @serverops/myserver-cli package lists them as optionalDependencies.
//
// We read artifacts.json instead of guessing directory names because
// Goreleaser v2's layout includes the build-id (e.g. `myserver_linux_amd64_v1`)
// and the GOAMD64 v1 suffix optionally — guessing led to a release
// failure on the first publish attempt (v0.1.0 dry run, 2026-05-22).

import { mkdirSync, readFileSync, writeFileSync, copyFileSync, existsSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..");
const distDir = join(root, "dist");
const outDir = join(root, "npm-packages");
const artifactsPath = join(distDir, "artifacts.json");

const rootPkg = JSON.parse(readFileSync(join(root, "package.json"), "utf8"));
const version = process.env.MYSERVER_CLI_VERSION || rootPkg.version;

if (!existsSync(artifactsPath)) {
  console.error(`MISSING ${artifactsPath} — goreleaser didn't produce artifacts metadata.`);
  process.exit(1);
}
const artifacts = JSON.parse(readFileSync(artifactsPath, "utf8"));

// goos+goarch (as goreleaser reports them) → npm os+cpu.
const platformMap = {
  "darwin/arm64": { os: "darwin", cpu: "arm64" },
  "darwin/amd64": { os: "darwin", cpu: "x64" },
  "linux/arm64":  { os: "linux",  cpu: "arm64" },
  "linux/amd64":  { os: "linux",  cpu: "x64" },
  "windows/amd64":{ os: "win32",  cpu: "x64" },
};

// Pick the raw Binary artifacts (type === "Binary"), one per goos+goarch.
// Goreleaser emits one Binary per (goos, goarch, goamd64) tuple; for
// amd64 the default is goamd64=v1 — that's the one we want.
const wanted = new Map(); // key → artifact
for (const a of artifacts) {
  if (a.type !== "Binary") continue;
  const key = `${a.goos}/${a.goarch}`;
  if (!platformMap[key]) continue;
  // Prefer v1 for amd64 when there are multiple variants; for arm64
  // there's only one. First match wins.
  if (a.goarch === "amd64" && a.goamd64 && a.goamd64 !== "v1") continue;
  if (!wanted.has(key)) wanted.set(key, a);
}

mkdirSync(outDir, { recursive: true });

let missing = 0;
for (const [key, mapping] of Object.entries(platformMap)) {
  const art = wanted.get(key);
  if (!art) {
    console.error(`MISSING ${mapping.os}-${mapping.cpu}: no Binary artifact for ${key} in artifacts.json`);
    missing++;
    continue;
  }
  const goBinAbs = join(root, art.path); // goreleaser uses repo-relative paths
  if (!existsSync(goBinAbs)) {
    console.error(`MISSING ${mapping.os}-${mapping.cpu}: artifact path ${art.path} not on disk`);
    missing++;
    continue;
  }

  const isWindows = mapping.os === "win32";
  const binName = isWindows ? "myserver.exe" : "myserver";
  const pkgName = `@serverops/myserver-cli-${mapping.os}-${mapping.cpu}`;
  const pkgDir = join(outDir, `${mapping.os}-${mapping.cpu}`);
  mkdirSync(join(pkgDir, "bin"), { recursive: true });

  copyFileSync(goBinAbs, join(pkgDir, "bin", binName));

  const platformPkg = {
    name: pkgName,
    version,
    description: `myserver-cli prebuilt binary for ${mapping.os}-${mapping.cpu}.`,
    license: rootPkg.license,
    homepage: rootPkg.homepage,
    repository: rootPkg.repository,
    files: ["bin/"],
    os: [mapping.os],
    cpu: [mapping.cpu],
    engines: rootPkg.engines,
  };
  writeFileSync(
    join(pkgDir, "package.json"),
    JSON.stringify(platformPkg, null, 2) + "\n",
  );

  writeFileSync(
    join(pkgDir, "README.md"),
    `# ${pkgName}\n\nPlatform-specific prebuilt binary for [@serverops/myserver-cli](https://www.npmjs.com/package/@serverops/myserver-cli).\n\nDo not depend on this package directly — install \`@serverops/myserver-cli\` and let npm pick the matching binary.\n`,
  );
  copyFileSync(join(root, "LICENSE"), join(pkgDir, "LICENSE"));

  console.log(`OK  ${pkgName}@${version}  ←  ${art.path}`);
}

if (missing > 0) {
  console.error(`\n${missing} platform binary/binaries missing — aborting.`);
  process.exit(1);
}
console.log(`\nAll platform packages staged in ${outDir}.`);
