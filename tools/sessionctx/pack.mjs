import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";

import { packageProvenance, resolveBuildTarget, validateReleaseManifest } from "./package-format.mjs";

const toolsDir = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(toolsDir, "../..");
const target = resolveBuildTarget();
const outputDir = path.join(root, "dist", "sessionctx", target.key);
const staging = path.join(outputDir, "staging");

function sha256File(file) {
  return crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
}

for (const required of ["package.json", "manifest.json", "run.cjs", "bin/cc-connect"]) {
  if (!fs.existsSync(path.join(staging, required))) throw new Error(`staging is incomplete: missing ${required}`);
}
const manifest = JSON.parse(fs.readFileSync(path.join(staging, "manifest.json"), "utf8"));
validateReleaseManifest(manifest, target);
const metadata = JSON.parse(fs.readFileSync(path.join(staging, "package.json"), "utf8"));
if (metadata.scripts?.postinstall || metadata.scripts?.install || metadata.dependencies) {
  throw new Error("staging package contains install hooks or dependencies")
}
if (fs.existsSync(path.join(staging, "install.js"))) throw new Error("upstream network install.js must not be staged");

for (const file of fs.readdirSync(outputDir)) {
  if (file.endsWith(".tgz")) fs.rmSync(path.join(outputDir, file));
}
execFileSync("pnpm", ["pack", "--pack-destination", outputDir], { cwd: staging, stdio: "inherit" });
const tarballs = fs.readdirSync(outputDir).filter((file) => file.endsWith(".tgz"));
if (tarballs.length !== 1) throw new Error(`expected one tarball, found ${tarballs.length}`);
const tarball = path.join(outputDir, tarballs[0]);
const contents = execFileSync("tar", ["-tzf", tarball], { encoding: "utf8" });
if (!contents.includes("package/bin/cc-connect") || !contents.includes("package/manifest.json") || contents.includes("install.js")) {
  throw new Error(`unexpected tarball contents:\n${contents}`);
}
const sha256 = sha256File(tarball);
fs.writeFileSync(`${tarball}.sha256`, `${sha256}  ${path.basename(tarball)}\n`, { mode: 0o600 });
const provenance = packageProvenance(manifest, path.basename(tarball), sha256);
fs.writeFileSync(path.join(outputDir, "package-provenance.json"), `${JSON.stringify(provenance, null, 2)}\n`, { mode: 0o600 });
console.log(tarball);
