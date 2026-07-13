import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { sessionctxTestPlan } from "./test-suite.mjs";
import { packageMetadata, resolveBuildTarget } from "./package-format.mjs";

const toolsDir = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(toolsDir, "../..");
const buildPath = path.join(toolsDir, "build.mjs");
const buildSource = fs.readFileSync(buildPath, "utf8");

test("sessionctx hard-gate plan is locked and ordered", () => {
  assert.deepEqual(sessionctxTestPlan.map((step) => step.id), [
    "diff-check",
    "scope-check",
    "compile-no-web",
    "config-feature",
    "codex-feature",
    "core-feature",
    "config-feature-race",
    "codex-feature-race",
    "core-feature-race",
    "offline-package",
  ]);
  assert.equal(sessionctxTestPlan.some((step) => step.command === "go" && step.args.includes("./...")), false);
  assert.equal(sessionctxTestPlan.some((step) => step.id === "core-feature" && step.args.some((arg) => arg.includes("CUJ_D8_"))), true);
  assert.equal(sessionctxTestPlan.some((step) => step.id === "codex-feature" && step.args.some((arg) => arg.includes("WithContext_"))), true);
});

test("build invokes hard gates after immutable-source preflight and before artifacts", () => {
  const tagGate = buildSource.indexOf("SESSIONCTX_RELEASE_TAG is required");
  const cleanGate = buildSource.indexOf('assertEqual("clean worktree"');
  const testGate = buildSource.indexOf('run("pnpm", ["run", "test:sessionctx"]');
  const webBuild = buildSource.indexOf('run("pnpm", ["--dir", "web", "install"');
  const goBuild = buildSource.indexOf("const goArgs =");
  assert.ok(tagGate >= 0 && cleanGate > tagGate && testGate > cleanGate && webBuild > testGate && goBuild > webBuild,
    `unexpected gate/build order: ${JSON.stringify({ tagGate, cleanGate, testGate, webBuild, goBuild })}`);
});

test("build fails before test or artifact work when release tag is absent", () => {
  const env = { ...process.env };
  delete env.SESSIONCTX_RELEASE_TAG;
  const result = spawnSync(process.execPath, [buildPath], { cwd: root, env, encoding: "utf8" });
  assert.notEqual(result.status, 0);
  const combined = `${result.stdout}\n${result.stderr}`;
  assert.match(combined, /SESSIONCTX_RELEASE_TAG is required/);
  assert.doesNotMatch(combined, /\[sessionctx gate\]/);
});

test("release manifest contract is v2 and does not bind a routing commit", () => {
  for (const marker of [
    "schema_version: 2",
    "release_ready: true",
    "routing_protocol_schema: 2",
    "provider_config_schema: 2",
    "release_tag: releaseTag",
    "release_commit: head",
  ]) {
    assert.ok(buildSource.includes(marker), `build manifest is missing ${marker}`);
  }
  assert.equal(/routing_commit\s*:/.test(buildSource), false);

  const linux = resolveBuildTarget({ SESSIONCTX_TARGET_GOOS: "linux", SESSIONCTX_TARGET_GOARCH: "amd64" });
  const metadata = packageMetadata("v", linux);
  assert.deepEqual(metadata.os, ["linux"]);
  assert.deepEqual(metadata.cpu, ["x64"]);
});
