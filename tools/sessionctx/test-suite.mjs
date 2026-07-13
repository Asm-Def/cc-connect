import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath, pathToFileURL } from "node:url";

const toolsDir = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(toolsDir, "../..");
export const sessionctxExactBase = "5d4c96dd12774574369e75b60084140101c9a59a";

const configPattern = "^TestCommandConfig_Direct";
const codexPattern = "^TestStartSession(WithContext_|_LegacyPath)";
const corePattern = "^Test(TokenizeDirectCommand_|ValidateDirectExecutable_|RunDirectExecutable_|DirectCommand_|CustomExec_LegacyShellCompatibility|ContextualStart_|SessionStartContext_|CUJ_D8_)";

// Order is part of the release contract. Build/package work must not start
// until every entry completes successfully.
export const sessionctxTestPlan = Object.freeze([
  { id: "diff-check", command: "git", args: ["diff", "--check", `${sessionctxExactBase}...HEAD`] },
  { id: "scope-check", command: "sh", args: [path.join(toolsDir, "scope-check.sh"), sessionctxExactBase, "HEAD"] },
  { id: "compile-no-web", command: "go", args: ["build", "-trimpath", "-tags", "no_web", "-o", "$SESSIONCTX_TEST_BIN", "./cmd/cc-connect"] },
  { id: "config-feature", command: "go", args: ["test", "./config", "-run", configPattern, "-count=1"] },
  { id: "codex-feature", command: "go", args: ["test", "./agent/codex", "-run", codexPattern, "-count=1"] },
  { id: "core-feature", command: "go", args: ["test", "./core", "-run", corePattern, "-count=1"] },
  { id: "config-feature-race", command: "go", args: ["test", "-race", "./config", "-run", configPattern, "-count=1"] },
  { id: "codex-feature-race", command: "go", args: ["test", "-race", "./agent/codex", "-run", codexPattern, "-count=1"] },
  { id: "core-feature-race", command: "go", args: ["test", "-race", "./core", "-run", corePattern, "-count=1"] },
  { id: "offline-package", command: "pnpm", args: ["run", "test:package"] },
]);

export function runSessionctxTestPlan() {
  const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), "cc-sessionctx-gate-"));
  const testBinary = path.join(tempDir, "cc-connect");
  try {
    for (const step of sessionctxTestPlan) {
      const args = step.args.map((arg) => arg === "$SESSIONCTX_TEST_BIN" ? testBinary : arg);
      console.log(`[sessionctx gate] ${step.id}: ${step.command} ${args.join(" ")}`);
      const result = spawnSync(step.command, args, { cwd: root, env: process.env, stdio: "inherit" });
      if (result.error) throw new Error(`${step.id}: failed to start: ${result.error.message}`);
      if (result.signal) throw new Error(`${step.id}: terminated by signal ${result.signal}`);
      if (result.status !== 0) throw new Error(`${step.id}: exited with status ${result.status}`);
    }
  } finally {
    fs.rmSync(tempDir, { recursive: true, force: true });
  }
}

const invokedAsMain = process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href;
if (invokedAsMain) {
  runSessionctxTestPlan();
  console.log("[sessionctx gate] all hard gates passed");
}
