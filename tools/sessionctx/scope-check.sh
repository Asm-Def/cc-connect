#!/bin/sh
set -eu

base="${1:-5d4c96dd12774574369e75b60084140101c9a59a}"
head="${2:-HEAD}"

changed="$(git diff --name-only "$base...$head")"
if [ "$head" = "HEAD" ]; then
  working="$(git diff --name-only; git diff --cached --name-only)"
  if [ -n "$working" ]; then
    changed="$(printf '%s\n%s\n' "$changed" "$working" | awk 'NF && !seen[$0]++')"
  fi
fi

for file in $changed; do
  case "$file" in
    agent/pi/*|agent/opencode/*|core/session.go|core/management.go|agent/codex/provider.go|agent/codex/provider_*.go)
      echo "scope violation: forbidden path changed: $file" >&2
      exit 1
      ;;
    agent/codex/codex.go|agent/codex/contextual_session_test.go|cmd/cc-connect/main.go|config/config.go|config/direct_command_test.go|core/command.go|core/direct_command.go|core/direct_executable_unix.go|core/direct_executable_windows.go|core/direct_command_test.go|core/contextual_session_test.go|core/cuj_test.go|core/engine.go|core/i18n.go|core/interfaces.go|core/session_namespace.go|package.json|pnpm-lock.yaml|tools/sessionctx/*)
      ;;
    *)
      echo "scope violation: path is outside the approved patch allowlist: $file" >&2
      exit 1
      ;;
  esac
done

if git diff --quiet "$base...$head" -- agent/pi agent/opencode; then
  :
else
  echo "scope violation: Pi/OpenCode must have zero diff" >&2
  exit 1
fi

printf 'scope check passed against %s (%s)\n' "$base" "$head"
