import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";

import { resolveBuildTarget, validateReleaseManifest, writePackageMetadata } from "./package-format.mjs";
import { sessionctxTestPlan } from "./test-suite.mjs";

const toolsDir = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(toolsDir, "../..");
const exactBase = "5d4c96dd12774574369e75b60084140101c9a59a";
const exactTag = "v1.4.1";
const target = resolveBuildTarget();
const staging = path.join(root, "dist", "sessionctx", target.key, "staging");

function run(command, args, options = {}) {
  const output = execFileSync(command, args, {
    cwd: options.cwd ?? root,
    env: options.env ?? process.env,
    encoding: options.encoding ?? "utf8",
    stdio: options.stdio ?? ["ignore", "pipe", "inherit"],
  });
  return typeof output === "string" ? output.trim() : "";
}

function sha256File(file) {
  return crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
}

function sha256Text(text) {
  return crypto.createHash("sha256").update(text).digest("hex");
}

function assertEqual(label, actual, expected) {
  if (actual !== expected) throw new Error(`${label}: got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
}

const releaseTag = process.env.SESSIONCTX_RELEASE_TAG;
if (!releaseTag) throw new Error("SESSIONCTX_RELEASE_TAG is required; builds from a moving branch are forbidden");
if (!/^cc-connect-sessionctx\/v1\.4\.1-r[1-9][0-9]*$/.test(releaseTag)) {
  throw new Error(`invalid release tag ${JSON.stringify(releaseTag)}`);
}

assertEqual("origin fetch URL", run("git", ["remote", "get-url", "origin"]), "https://github.com/Asm-Def/cc-connect.git");
assertEqual("origin push URL", run("git", ["remote", "get-url", "--push", "origin"]), "https://github.com/Asm-Def/cc-connect.git");
assertEqual("upstream fetch URL", run("git", ["remote", "get-url", "upstream"]), "https://github.com/chenhg5/cc-connect.git");
assertEqual("upstream push URL", run("git", ["remote", "get-url", "--push", "upstream"]), "DISABLED");
assertEqual("upstream base tag", run("git", ["rev-parse", `${exactTag}^{}`]), exactBase);
assertEqual("release tag object type", run("git", ["cat-file", "-t", releaseTag]), "tag");
const head = run("git", ["rev-parse", "HEAD"]);
assertEqual("release tag peeled commit", run("git", ["rev-parse", `${releaseTag}^{}`]), head);
assertEqual("clean worktree", run("git", ["status", "--porcelain=v1"]), "");
run("git", ["merge-base", "--is-ancestor", exactBase, head]);

// Hard feature gates run after immutable-source/clean checks and before web,
// release binary, or staging work. A failure therefore cannot produce a new
// release staging directory.
run("pnpm", ["run", "test:sessionctx"], { stdio: "inherit" });

const releaseNumber = releaseTag.match(/-r([0-9]+)$/)[1];
const shortCommit = head.slice(0, 12);
const version = `1.4.1-sessionctx.${releaseNumber}+g${shortCommit}`;
const sourceDate = run("git", ["show", "-s", "--format=%cI", "HEAD"]);

// The web lock is authoritative: frozen install first, then its existing build.
run("pnpm", ["--dir", "web", "install", "--frozen-lockfile"], { stdio: "inherit" });
run("pnpm", ["--dir", "web", "run", "build"], { stdio: "inherit" });

fs.rmSync(staging, { recursive: true, force: true });
fs.mkdirSync(path.join(staging, "bin"), { recursive: true, mode: 0o700 });
const binaryPath = path.join(staging, "bin", "cc-connect");
const ldflags = `-s -w -X main.version=${version} -X main.commit=${head} -X main.buildTime=${sourceDate}`;
const goArgs = ["build", "-trimpath", "-buildvcs=true", "-ldflags", ldflags, "-o", binaryPath, "./cmd/cc-connect"];
run("go", goArgs, {
  env: { ...process.env, GOOS: target.goos, GOARCH: target.goarch, CGO_ENABLED: "0", SOURCE_DATE_EPOCH: String(Date.parse(sourceDate) / 1000) },
  stdio: "inherit",
});
fs.chmodSync(binaryPath, 0o755);

writePackageMetadata(staging, version, target);
fs.copyFileSync(path.join(toolsDir, "package-runner.cjs"), path.join(staging, "run.cjs"));
fs.chmodSync(path.join(staging, "run.cjs"), 0o755);
fs.copyFileSync(path.join(toolsDir, "README.md"), path.join(staging, "README.md"));

const diff = run("git", ["diff", "--binary", `${exactBase}...${head}`]);
const manifest = {
  schema_version: 2,
  release_ready: true,
  routing_protocol_schema: 2,
  provider_config_schema: 2,
  package: { name: "cc-connect", version, platform: target.nodePlatform, arch: target.nodeArch, offline: true },
  source: {
    origin: "https://github.com/Asm-Def/cc-connect.git",
    upstream: "https://github.com/chenhg5/cc-connect.git",
    upstream_tag: exactTag,
    upstream_commit: exactBase,
    fork_commit: head,
    release_tag: releaseTag,
    release_commit: head,
    patch_diff_sha256: sha256Text(diff),
    range_diff_sha256: null,
    old_to_new_commit_mapping: "initial patch stack; no upstream rebase",
  },
  toolchain: {
    node: process.version,
    pnpm: run("pnpm", ["--version"]),
    go: run("go", ["version"]),
    root_lock_sha256: sha256File(path.join(root, "pnpm-lock.yaml")),
    web_lock_sha256: sha256File(path.join(root, "web", "pnpm-lock.yaml")),
  },
  build: {
    source_date: sourceDate,
    goos: target.goos,
    goarch: target.goarch,
    cgo_enabled: "0",
    flags: goArgs,
  },
  validation: {
    entrypoint: "pnpm run test:sessionctx",
    hard_gate_before_web_binary_and_staging: true,
    plan: sessionctxTestPlan.map((step) => step.id),
    test_suite_sha256: sha256File(path.join(toolsDir, "test-suite.mjs")),
    baseline_exceptions: [
      "upstream v1.4.1 asynchronous TempDir/session-save cleanup race (including CUJ-A5 and release-local media)",
      "pre-existing full-parallel timing flakes in Codex runtime-config and iFlow timer tests; isolated repeated runs pass",
    ],
  },
  binary: { path: "bin/cc-connect", sha256: sha256File(binaryPath) },
};
validateReleaseManifest(manifest, target);
fs.writeFileSync(path.join(staging, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`, { mode: 0o600 });
console.log(staging);
