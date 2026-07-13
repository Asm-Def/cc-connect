# cc-connect session-context fork build gate

`pnpm run test:sessionctx` is the locked hard gate for this fork patch. It runs, in order:

1. patch diff validation and the approved-path scope guard;
2. a `no_web` compile of `cmd/cc-connect`;
3. direct-command config tests;
4. Codex contextual, legacy, and app-server compatibility tests;
5. core direct-command, contextual-resume, and CUJ-D8 tests;
6. race builds of all feature-specific Go tests;
7. offline package/runner tests, including missing or mismatched checksum failure.

Set `SESSIONCTX_TARGET_GOOS` and `SESSIONCTX_TARGET_GOARCH` to build `darwin/arm64` or the Station's
`linux/arm64|amd64` target from the same release commit. Each target is staged independently under
`dist/sessionctx/<goos>-<goarch>/`.

`pnpm run build` first verifies the immutable annotated release tag, exact upstream base, remotes, and
clean worktree. It then invokes this gate before installing/building web assets, compiling the release
binary, or creating staging. A gate failure therefore cannot leave a new release staging package.

## Upstream baseline exceptions

The release gate intentionally does not use `go test ./...` or the complete CUJ inventory as a hard
condition. At exact upstream `v1.4.1 / 5d4c96dd`, independent baseline runs reproduce asynchronous
TempDir/session-save cleanup failures such as `TestCUJ_A5_FileReachesAgent`; the same family can affect
the release-local media pipeline. Full parallel runs also expose pre-existing timing flakes in the
Codex runtime-config and iFlow timer tests, while isolated repeated runs pass.

These exceptions are not waivers for this fork feature. Every direct/session-exclusive/contextual
test, CUJ-D8, app-server legacy-fallback test, race build, scope check, and offline package test remains
a mandatory hard gate. The broader upstream suite is still diagnostic evidence and must be reviewed
separately during an upstream rebase.
