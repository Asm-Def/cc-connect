import fs from "node:fs";
import path from "node:path";

export function resolveBuildTarget(env = process.env, hostPlatform = process.platform, hostArch = process.arch) {
  const goos = env.SESSIONCTX_TARGET_GOOS || hostPlatform;
  const defaultGoarch = hostArch === "x64" ? "amd64" : hostArch;
  const goarch = env.SESSIONCTX_TARGET_GOARCH || defaultGoarch;
  if (goos !== "darwin" && goos !== "linux") {
    throw new Error(`unsupported SESSIONCTX_TARGET_GOOS ${JSON.stringify(goos)}; expected darwin or linux`);
  }
  if (goarch !== "arm64" && goarch !== "amd64") {
    throw new Error(`unsupported SESSIONCTX_TARGET_GOARCH ${JSON.stringify(goarch)}; expected arm64 or amd64`);
  }
  if (goos === "darwin" && goarch !== "arm64") {
    throw new Error("this fork release supports darwin/arm64 only");
  }
  const nodeArch = goarch === "amd64" ? "x64" : goarch;
  return Object.freeze({ goos, goarch, nodePlatform: goos, nodeArch, key: `${goos}-${goarch}` });
}

export function packageMetadata(version, target) {
  return {
    name: "cc-connect",
    version,
    description: "Rebaseable cc-connect session-context fork build",
    private: true,
    license: "MIT",
    repository: {
      type: "git",
      url: "https://github.com/Asm-Def/cc-connect.git",
    },
    os: [target.nodePlatform],
    cpu: [target.nodeArch],
    engines: { node: ">=20" },
    bin: { "cc-connect": "run.cjs" },
    files: ["bin/cc-connect", "manifest.json", "run.cjs", "README.md"],
  };
}

export function writePackageMetadata(stagingDir, version, target) {
  const metadata = packageMetadata(version, target);
  fs.writeFileSync(path.join(stagingDir, "package.json"), `${JSON.stringify(metadata, null, 2)}\n`, { mode: 0o600 });
  return metadata;
}

export function validateReleaseManifest(manifest, target) {
  if (manifest?.schema_version !== 2 || manifest?.release_ready !== true) {
    throw new Error("release manifest is not release-ready schema v2");
  }
  if (manifest.routing_protocol_schema !== 2 || manifest.provider_config_schema !== 2) {
    throw new Error("release manifest compatibility schemas must both be v2");
  }
  if (!manifest.source || typeof manifest.source.release_tag !== "string" || !/^[a-f0-9]{40}$/.test(manifest.source.release_commit || "")) {
    throw new Error("release manifest source tag/commit is missing or invalid");
  }
  if (Object.hasOwn(manifest.source, "routing_commit") || Object.hasOwn(manifest, "routing_commit")) {
    throw new Error("cc-connect release manifest must not bind a routing commit");
  }
  if (manifest.package?.platform !== target.nodePlatform || manifest.package?.arch !== target.nodeArch) {
    throw new Error(`release manifest target does not match ${target.nodePlatform}/${target.nodeArch}`);
  }
  if (!manifest.binary || !/^[a-f0-9]{64}$/.test(manifest.binary.sha256 || "")) {
    throw new Error("release manifest binary checksum is missing or invalid");
  }
  return manifest;
}

export function packageProvenance(manifest, tarball, packageSha256) {
  const provenance = {
    schema_version: 2,
    tarball,
    package_sha256: packageSha256,
    binary_sha256: manifest.binary.sha256,
    fork_commit: manifest.source.fork_commit,
    release_tag: manifest.source.release_tag,
    release_commit: manifest.source.release_commit,
    release_ready: manifest.release_ready,
    routing_protocol_schema: manifest.routing_protocol_schema,
    provider_config_schema: manifest.provider_config_schema,
    platform: manifest.package.platform,
    arch: manifest.package.arch,
  };
  if (!/^[a-f0-9]{64}$/.test(provenance.package_sha256 || "") ||
      !/^[a-f0-9]{64}$/.test(provenance.binary_sha256 || "") ||
      !/^[a-f0-9]{40}$/.test(provenance.release_commit || "") ||
      provenance.release_ready !== true || provenance.routing_protocol_schema !== 2 || provenance.provider_config_schema !== 2) {
    throw new Error("package provenance is incomplete or invalid");
  }
  return provenance;
}
