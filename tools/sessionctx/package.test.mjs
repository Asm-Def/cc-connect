import assert from "node:assert/strict";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { execFileSync, spawnSync } from "node:child_process";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { packageMetadata, packageProvenance, resolveBuildTarget, validateReleaseManifest } from "./package-format.mjs";

const toolsDir = path.dirname(fileURLToPath(import.meta.url));
const runnerSource = fs.readFileSync(path.join(toolsDir, "package-runner.cjs"), "utf8");
const hostTarget = resolveBuildTarget({}, process.platform, process.arch);

function sha256(data) {
  return crypto.createHash("sha256").update(data).digest("hex");
}

function createFixture(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "cc-sessionctx-package-"));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  fs.mkdirSync(path.join(dir, "bin"), { recursive: true });
  const binary = Buffer.from("#!/bin/sh\nprintf 'fixture:%s\\n' \"$1\"\n");
  fs.writeFileSync(path.join(dir, "bin", "cc-connect"), binary, { mode: 0o644 });
  fs.writeFileSync(path.join(dir, "run.cjs"), runnerSource, { mode: 0o755 });
  fs.writeFileSync(path.join(dir, "manifest.json"), `${JSON.stringify({
    schema_version: 2,
    release_ready: true,
    routing_protocol_schema: 2,
    provider_config_schema: 2,
    package: { platform: hostTarget.nodePlatform, arch: hostTarget.nodeArch },
    source: { release_tag: "cc-connect-sessionctx/v1.4.1-r1", release_commit: "a".repeat(40), fork_commit: "a".repeat(40) },
    binary: { sha256: sha256(binary) },
  })}\n`);
  fs.writeFileSync(path.join(dir, "package.json"), `${JSON.stringify(packageMetadata("1.4.1-sessionctx.1+gtest", hostTarget), null, 2)}\n`);
  fs.writeFileSync(path.join(dir, "README.md"), "fixture\n");
  return dir;
}

test("offline package metadata has no install hook or dependency", () => {
  const metadata = packageMetadata("1.4.1-sessionctx.1+gtest", hostTarget);
  assert.equal(metadata.scripts, undefined);
  assert.equal(metadata.dependencies, undefined);
  assert.deepEqual(metadata.files, ["bin/cc-connect", "manifest.json", "run.cjs", "README.md"]);
  for (const forbidden of ["install.js", "postinstall", "node:https", "node:http", "fetch("]) {
    assert.equal(runnerSource.includes(forbidden), false, `runner contains forbidden network/fallback marker ${forbidden}`);
  }
});

test("release manifest and package provenance contracts are mechanically validated", (t) => {
  const dir = createFixture(t);
  const manifest = JSON.parse(fs.readFileSync(path.join(dir, "manifest.json"), "utf8"));
  assert.equal(validateReleaseManifest(manifest, hostTarget), manifest);
  const provenance = packageProvenance(manifest, "cc-connect.tgz", "b".repeat(64));
  assert.equal(provenance.release_ready, true);
  assert.equal(provenance.routing_protocol_schema, 2);
  assert.equal(provenance.provider_config_schema, 2);
  assert.equal(provenance.release_commit, "a".repeat(40));

  assert.throws(() => validateReleaseManifest({ ...manifest, release_ready: false }, hostTarget), /release-ready/);
  assert.throws(() => validateReleaseManifest({ ...manifest, routing_commit: "forbidden" }, hostTarget), /must not bind/);
  assert.throws(() => packageProvenance(manifest, "cc-connect.tgz", "bad"), /provenance/);
});

test("runner executes only a checksum-verified embedded binary", (t) => {
  const dir = createFixture(t);
  const ok = spawnSync(process.execPath, [path.join(dir, "run.cjs"), "ok"], { encoding: "utf8" });
  assert.equal(ok.status, 0, ok.stderr);
  assert.equal(ok.stdout, "fixture:ok\n");
  assert.equal(fs.statSync(path.join(dir, "bin", "cc-connect")).mode & 0o777, 0o500);

  fs.chmodSync(path.join(dir, "bin", "cc-connect"), 0o644);
  fs.appendFileSync(path.join(dir, "bin", "cc-connect"), "tampered\n");
  const mismatch = spawnSync(process.execPath, [path.join(dir, "run.cjs")], { encoding: "utf8" });
  assert.notEqual(mismatch.status, 0);
  assert.match(mismatch.stderr, /checksum mismatch/);
  assert.equal(fs.statSync(path.join(dir, "bin", "cc-connect")).mode & 0o777, 0o644,
    "tampered payload must not be chmodded");
});

test("package metadata and runner target are platform-parameterized", (t) => {
  const darwin = resolveBuildTarget({ SESSIONCTX_TARGET_GOOS: "darwin", SESSIONCTX_TARGET_GOARCH: "arm64" });
  const linuxArm = resolveBuildTarget({ SESSIONCTX_TARGET_GOOS: "linux", SESSIONCTX_TARGET_GOARCH: "arm64" });
  const linuxAMD = resolveBuildTarget({ SESSIONCTX_TARGET_GOOS: "linux", SESSIONCTX_TARGET_GOARCH: "amd64" });
  assert.deepEqual(packageMetadata("v", darwin).os, ["darwin"]);
  assert.deepEqual(packageMetadata("v", linuxArm).cpu, ["arm64"]);
  assert.deepEqual(packageMetadata("v", linuxAMD).cpu, ["x64"]);

  const dir = createFixture(t);
  const manifestPath = path.join(dir, "manifest.json");
  const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
  manifest.package.arch = manifest.package.arch === "arm64" ? "x64" : "arm64";
  fs.writeFileSync(manifestPath, `${JSON.stringify(manifest)}\n`);
  const mismatch = spawnSync(process.execPath, [path.join(dir, "run.cjs")], { encoding: "utf8" });
  assert.notEqual(mismatch.status, 0);
  assert.match(mismatch.stderr, /package is for/);
});

test("runner fails closed for missing binary or checksum", (t) => {
  const dir = createFixture(t);
  fs.rmSync(path.join(dir, "bin", "cc-connect"));
  const missing = spawnSync(process.execPath, [path.join(dir, "run.cjs")], { encoding: "utf8" });
  assert.notEqual(missing.status, 0);
  assert.match(missing.stderr, /binary is missing/);

  fs.writeFileSync(path.join(dir, "bin", "cc-connect"), "x", { mode: 0o644 });
  fs.writeFileSync(path.join(dir, "manifest.json"), "{}\n");
  const noChecksum = spawnSync(process.execPath, [path.join(dir, "run.cjs")], { encoding: "utf8" });
  assert.notEqual(noChecksum.status, 0);
  assert.match(noChecksum.stderr, /checksum is missing or invalid/);
  assert.equal(fs.statSync(path.join(dir, "bin", "cc-connect")).mode & 0o777, 0o644,
    "payload without checksum must not be chmodded");
});

test("real tarball installs globally with pnpm --ignore-scripts and executes --version", (t) => {
  const dir = createFixture(t);
  const out = fs.mkdtempSync(path.join(os.tmpdir(), "cc-sessionctx-pack-"));
  const prefix = fs.mkdtempSync(path.join(os.tmpdir(), "cc-sessionctx-global-"));
  t.after(() => fs.rmSync(out, { recursive: true, force: true }));
  t.after(() => fs.rmSync(prefix, { recursive: true, force: true }));
  execFileSync("pnpm", ["pack", "--pack-destination", out], { cwd: dir, stdio: "pipe" });
  const tarball = path.join(out, fs.readdirSync(out).find((file) => file.endsWith(".tgz")));
  const globalDir = path.join(prefix, "global");
  const binDir = path.join(prefix, "bin");
  fs.mkdirSync(globalDir, { recursive: true });
  fs.mkdirSync(binDir, { recursive: true });
  const isolatedEnv = {
    ...process.env,
    PNPM_HOME: binDir,
    PATH: `${binDir}${path.delimiter}${process.env.PATH || ""}`,
    npm_config_registry: "http://127.0.0.1:9",
    HTTP_PROXY: "http://127.0.0.1:9",
    HTTPS_PROXY: "http://127.0.0.1:9",
    NO_PROXY: "",
  };
  execFileSync("pnpm", ["add", "--global", "--global-dir", globalDir, "--global-bin-dir", binDir,
    "--ignore-scripts", "--offline", "--force", tarball], {
    cwd: prefix,
    stdio: "pipe",
    env: isolatedEnv,
  });
  const installed = path.join(globalDir, "5", "node_modules", "cc-connect");
  assert.equal(fs.existsSync(path.join(installed, "bin", "cc-connect")), true);
  assert.equal(fs.existsSync(path.join(installed, "install.js")), false);
  const installedMetadata = JSON.parse(fs.readFileSync(path.join(installed, "package.json"), "utf8"));
  assert.equal(installedMetadata.scripts?.postinstall, undefined);
  assert.equal(fs.statSync(path.join(installed, "bin", "cc-connect")).mode & 0o111, 0,
    "isolated pnpm install should reproduce the non-executable embedded payload");

  const version = spawnSync(path.join(binDir, "cc-connect"), ["--version"], {
    cwd: prefix, env: isolatedEnv, encoding: "utf8",
  });
  assert.equal(version.status, 0, version.stderr);
  assert.equal(version.stdout, "fixture:--version\n");
  assert.equal(fs.statSync(path.join(installed, "bin", "cc-connect")).mode & 0o777, 0o500);
});
