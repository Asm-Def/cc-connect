#!/usr/bin/env node

"use strict";

const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");
const { spawnSync } = require("node:child_process");

function fail(message) {
  console.error(`[cc-connect-sessionctx] ${message}`);
  process.exit(1);
}

const manifestPath = path.join(__dirname, "manifest.json");
const binaryPath = path.join(__dirname, "bin", "cc-connect");
if (!fs.existsSync(manifestPath)) fail("manifest.json is missing; refusing to run");
if (!fs.existsSync(binaryPath)) fail("embedded binary is missing; refusing to run");

let manifest;
try {
  manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
} catch (error) {
  fail(`manifest.json is invalid: ${error.message}`);
}
if (!manifest.binary || !/^[a-f0-9]{64}$/.test(manifest.binary.sha256 || "")) {
  fail("binary checksum is missing or invalid; refusing to run");
}
if (!manifest.package || typeof manifest.package.platform !== "string" || typeof manifest.package.arch !== "string") {
  fail("package target is missing or invalid; refusing to run");
}
if (manifest.schema_version !== 2 || manifest.release_ready !== true ||
    manifest.routing_protocol_schema !== 2 || manifest.provider_config_schema !== 2 ||
    !manifest.source || typeof manifest.source.release_tag !== "string" ||
    !/^[a-f0-9]{40}$/.test(manifest.source.release_commit || "")) {
  fail("release provenance is missing or invalid; refusing to run");
}

const stat = fs.lstatSync(binaryPath);
if (!stat.isFile() || stat.isSymbolicLink()) fail("embedded binary must be a regular non-symlink file");
const actual = crypto.createHash("sha256").update(fs.readFileSync(binaryPath)).digest("hex");
if (actual !== manifest.binary.sha256) fail("embedded binary checksum mismatch; refusing to run");
if (process.platform !== manifest.package.platform || process.arch !== manifest.package.arch) {
  fail(`package is for ${manifest.package.platform}/${manifest.package.arch}, got ${process.platform}/${process.arch}`);
}

const result = spawnSync(binaryPath, process.argv.slice(2), { stdio: "inherit" });
if (result.error) fail(`failed to execute embedded binary: ${result.error.message}`);
if (result.signal) {
  process.kill(process.pid, result.signal);
}
process.exit(result.status ?? 1);
