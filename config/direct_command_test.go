package config

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func parseDirectCommandConfig(t *testing.T, command string) *Config {
	t.Helper()
	text := `
[[projects]]
name = "test"
[projects.agent]
type = "codex"
` + command
	cfg := &Config{}
	if _, err := toml.Decode(text, cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return cfg
}

func TestCommandConfig_DirectModeParsesAndValidates(t *testing.T) {
	cfg := parseDirectCommandConfig(t, `
[[commands]]
name = "route"
exec = "/home/user/bin/provider-router-ctl"
exec_mode = "direct"
session_exclusive = true
`)
	if err := cfg.validatePermissive(); err != nil {
		t.Fatalf("validate direct command: %v", err)
	}
	if len(cfg.Commands) != 1 || cfg.Commands[0].ExecMode != "direct" || !cfg.Commands[0].SessionExclusive {
		t.Fatalf("decoded command = %#v", cfg.Commands)
	}
}

func TestCommandConfig_DirectModeRejectsUnsafeShapes(t *testing.T) {
	tests := []struct {
		name    string
		command string
		wantErr string
	}{
		{
			name: "unknown mode", wantErr: "exec_mode",
			command: `
[[commands]]
name = "route"
exec = "/bin/route"
exec_mode = "argv-ish"
`,
		},
		{
			name: "exclusive legacy shell", wantErr: "requires exec_mode",
			command: `
[[commands]]
name = "route"
exec = "route {{args}}"
session_exclusive = true
`,
		},
		{
			name: "template", wantErr: "forbids exec templates",
			command: `
[[commands]]
name = "route"
exec = "/bin/route/{{args}}"
exec_mode = "direct"
`,
		},
		{
			name: "prompt mixed with direct", wantErr: "forbids prompt",
			command: `
[[commands]]
name = "route"
exec = "/bin/route"
prompt = "do it"
exec_mode = "direct"
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parseDirectCommandConfig(t, tt.command).validatePermissive()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}
