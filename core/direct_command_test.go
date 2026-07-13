package core

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func secureTestExecutable(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("direct command deployment is Unix-only")
	}
	dir, err := os.MkdirTemp(".", ".cc-direct-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := filepath.Abs(filepath.Join(dir, "command"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTokenizeDirectCommand_StrictRawArgv(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
		err  bool
	}{
		{name: "tokens", raw: "default newapi", want: []string{"default", "newapi"}},
		{name: "tabs and repeated separators", raw: "  status\t\treq-1  ", want: []string{"status", "req-1"}},
		{name: "metacharacter stays one argument", raw: "name;touch$IFS/tmp/pwn", want: []string{"name;touch$IFS/tmp/pwn"}},
		{name: "matched quote rejected", raw: `"name"`, err: true},
		{name: "unmatched quote rejected", raw: `'name`, err: true},
		{name: "empty quoted rejected", raw: `""`, err: true},
		{name: "backslash rejected", raw: `name\ value`, err: true},
		{name: "control rejected", raw: "name\nother", err: true},
		{name: "zero arguments rejected", raw: " \t ", err: true},
		{name: "too many rejected", raw: "1 2 3 4 5 6 7 8 9", err: true},
		{name: "long rejected", raw: strings.Repeat("a", directCommandMaxArgLen+1), err: true},
		{name: "invalid utf8 rejected", raw: string([]byte{0xff}), err: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tokenizeDirectCommand(tt.raw)
			if tt.err {
				if err == nil {
					t.Fatalf("tokenizeDirectCommand(%q) unexpectedly succeeded: %#v", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("tokenizeDirectCommand(%q): %v", tt.raw, err)
			}
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestValidateDirectExecutable_SecurityBoundaries(t *testing.T) {
	valid := secureTestExecutable(t, "echo ok")
	if err := validateDirectExecutable(valid); err != nil {
		t.Fatalf("valid executable rejected: %v", err)
	}
	if err := validateDirectExecutable("relative-command"); err == nil {
		t.Fatal("relative executable accepted")
	}
	if err := validateDirectExecutable(valid + ";other"); err == nil {
		t.Fatal("shell-like executable string accepted")
	}

	symlink := valid + "-link"
	if err := os.Symlink(valid, symlink); err != nil {
		t.Fatal(err)
	}
	if err := validateDirectExecutable(symlink); err == nil {
		t.Fatal("symlink executable accepted")
	}

	parent := filepath.Join(filepath.Dir(valid), "world-writable")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	unsafe := filepath.Join(parent, "command")
	if err := os.WriteFile(unsafe, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateDirectExecutable(unsafe); err == nil || !strings.Contains(err.Error(), "group- or world-writable") {
		t.Fatalf("world-writable parent error = %v", err)
	}

	groupParent := filepath.Join(filepath.Dir(valid), "group-writable")
	if err := os.Mkdir(groupParent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(groupParent, 0o770); err != nil {
		t.Fatal(err)
	}
	groupUnsafe := filepath.Join(groupParent, "command")
	if err := os.WriteFile(groupUnsafe, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateDirectExecutable(groupUnsafe); err == nil || !strings.Contains(err.Error(), "group- or world-writable") {
		t.Fatalf("group-writable parent error = %v", err)
	}

}

func TestValidateDirectExecutable_ForeignUID0755Parent(t *testing.T) {
	const parentMode = os.FileMode(0o755)
	if parentMode.Perm()&0o022 != 0 {
		t.Fatal("test fixture must model a non-writable 0755 parent")
	}
	if err := validateDirectOwnerID(1234, 4321, "executable parent"); err == nil || !strings.Contains(err.Error(), "current user or root") {
		t.Fatalf("foreign-owned 0755 parent accepted: %v", err)
	}
	if err := validateDirectOwnerID(0, 4321, "executable parent"); err != nil {
		t.Fatalf("root-owned 0755 parent rejected: %v", err)
	}
	if err := validateDirectOwnerID(4321, 4321, "executable parent"); err != nil {
		t.Fatalf("euid-owned 0755 parent rejected: %v", err)
	}
}

func TestRunDirectExecutable_NoShellNoInheritedSecretsAndBoundedOutput(t *testing.T) {
	command := secureTestExecutable(t, `
printf 'arg=%s\n' "$1"
printf 'secret=%s\n' "${UPSTREAM_SECRET-unset}"
i=0
while [ "$i" -lt 20000 ]; do printf x; i=$((i + 1)); done
`)
	t.Setenv("UPSTREAM_SECRET", "must-not-leak")
	marker := filepath.Join(filepath.Dir(command), "pwned")
	arg := "safe;touch$IFS" + marker
	result := runDirectExecutable(context.Background(), command, []string{arg}, filepath.Dir(command), []string{"CC_PROJECT=test"})
	if result.WaitErr != nil {
		t.Fatalf("runDirectExecutable: %v", result.WaitErr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("shell metacharacter executed; marker stat error = %v", err)
	}
	if !strings.Contains(result.Output, "arg="+arg) {
		t.Fatalf("argument was not passed literally: %q", result.Output)
	}
	if !strings.Contains(result.Output, "secret=unset") || strings.Contains(result.Output, "must-not-leak") {
		t.Fatalf("daemon secret leaked into direct env: %q", result.Output)
	}
	if !result.Truncated || len(result.Output) > directCommandMaxCapture {
		t.Fatalf("bounded capture = (len=%d, truncated=%v)", len(result.Output), result.Truncated)
	}
}

func TestRunDirectExecutable_TimeoutAndOutputRedaction(t *testing.T) {
	command := secureTestExecutable(t, `while :; do :; done`)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result := runDirectExecutable(ctx, command, nil, filepath.Dir(command), nil)
	if !result.TimedOut {
		t.Fatalf("TimedOut = false, result = %#v", result)
	}

	redacted := redactDirectCommandOutput("api_key=sk-secret Bearer abc123 password: hunter2")
	for _, secret := range []string{"sk-secret", "abc123", "hunter2"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("secret %q not redacted from %q", secret, redacted)
		}
	}
}

func TestDirectCommand_AdminDeniedBeforeExecutableValidation(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("admin-only")
	e.AddCommandWithOptions("route", "", "", "/does/not/exist", "", "config", CustomCommandOptions{ExecMode: "direct", SessionExclusive: true})

	e.ReceiveMessage(p, &Message{
		SessionKey: "test:member", Platform: "test", UserID: "member", UserName: "member",
		Content: "/route value", ReplyCtx: "ctx",
	})
	sent := p.getSent()
	if len(sent) != 1 || !strings.Contains(strings.ToLower(sent[0]), "admin") {
		t.Fatalf("non-admin response = %#v", sent)
	}
	if strings.Contains(sent[0], "executable") || strings.Contains(sent[0], "stat") {
		t.Fatalf("path validation ran before authorization: %q", sent[0])
	}
}

func TestDirectCommand_MissingPersistentStoreFailsClosed(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	command := secureTestExecutable(t, `touch "$1"`)
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("*")
	e.AddCommandWithOptions("route", "", "", command, "", "config", CustomCommandOptions{ExecMode: "direct", SessionExclusive: true})
	e.ReceiveMessage(p, &Message{
		SessionKey: "test:user", Platform: "test", UserID: "user", UserName: "user",
		Content: "/route " + marker, ReplyCtx: "ctx",
	})
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("direct command ran without stable namespace; marker stat=%v", err)
	}
	if sent := strings.Join(p.getSent(), "\n"); !strings.Contains(sent, "stable persistent session namespace unavailable") {
		t.Fatalf("missing fail-closed response: %q", sent)
	}
}

func TestDirectCommand_InvalidUTF8StorePathFailsClosed(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	command := secureTestExecutable(t, `touch "$1"`)
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, invalidUTF8SessionStorePath(t), LangEnglish)
	e.SetAdminFrom("*")
	e.AddCommandWithOptions("route", "", "", command, "", "config", CustomCommandOptions{ExecMode: "direct", SessionExclusive: true})
	e.ReceiveMessage(p, &Message{
		SessionKey: "test:user", Platform: "test", UserID: "user", UserName: "user",
		Content: "/route " + marker, ReplyCtx: "ctx",
	})
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("direct command ran with invalid UTF-8 store path; marker stat=%v", err)
	}
	if sent := strings.Join(p.getSent(), "\n"); !strings.Contains(sent, "stable persistent session namespace unavailable") {
		t.Fatalf("missing fail-closed response: %q", sent)
	}
}

func TestCustomExec_LegacyShellCompatibility(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test command uses POSIX shell")
	}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("*")
	marker := filepath.Join(t.TempDir(), "legacy-output")
	e.AddCommand("legacy", "", "", "printf legacy > "+marker, "", "config")
	e.ReceiveMessage(p, &Message{
		SessionKey: "test:user", Platform: "test", UserID: "user", UserName: "user",
		Content: "/legacy", ReplyCtx: "ctx",
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(marker); err == nil && string(data) == "legacy" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("legacy shell command did not run; replies=%v", p.getSent())
}

func TestDirectCommand_WorkspaceLogicalSessionContext(t *testing.T) {
	command := secureTestExecutable(t, `set > "$1"`)
	baseDir := t.TempDir()
	workspace := normalizeWorkspacePath(t.TempDir())
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("project-a", &stubAgent{}, []Platform{p}, filepath.Join(t.TempDir(), "global.json"), LangEnglish)
	e.SetAdminFrom("*")
	e.SetDataDir("/configured/data")
	e.SetMultiWorkspace(baseDir, filepath.Join(t.TempDir(), "bindings.json"))
	channelID := "C1"
	e.workspaceBindings.Bind("project:project-a", channelID, "channel", workspace)
	ws := e.workspacePool.GetOrCreate(workspace)
	ws.agent = &stubAgent{}
	ws.sessions = NewSessionManager(filepath.Join(t.TempDir(), "workspace-sessions.json"))
	e.AddCommandWithOptions("ctx", "", "", command, "", "config", CustomCommandOptions{ExecMode: "direct", SessionExclusive: true})

	output := filepath.Join(t.TempDir(), "context.env")
	sessionKey := "test:" + channelID + ":user"
	e.ReceiveMessage(p, &Message{
		SessionKey: sessionKey, ChannelID: channelID, Platform: "test", UserID: "user", UserName: "user",
		Content: "/ctx " + output, ReplyCtx: "ctx",
	})

	deadline := time.Now().Add(2 * time.Second)
	var data []byte
	for time.Now().Before(deadline) {
		data, _ = os.ReadFile(output)
		if len(data) != 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(data) == 0 {
		t.Fatalf("direct command did not write context; replies=%v", p.getSent())
	}
	logical := ws.sessions.GetOrCreateActive(sessionKey)
	wantNamespace, err := SessionNamespace(ws.sessions)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, entry := range []string{
		"CC_PROJECT=project-a", "CC_SESSION_KEY=" + sessionKey,
		"CC_SESSION_NAMESPACE=" + wantNamespace,
		"CC_LOGICAL_SESSION_ID=" + logical.ID, "CC_AGENT_TYPE=stub", "CC_DATA_DIR=/configured/data",
	} {
		if !strings.Contains(got, entry+"\n") {
			t.Errorf("context missing %q in %q", entry, got)
		}
	}
	if history := logical.GetHistory(0); len(history) != 0 {
		t.Fatalf("direct command entered LLM history: %#v", history)
	}
}

func TestDirectCommand_WorkspaceNamespaceSeparatesS1AndChangesOnRebind(t *testing.T) {
	baseDir := t.TempDir()
	workspace1 := normalizeWorkspacePath(t.TempDir())
	workspace2 := normalizeWorkspacePath(t.TempDir())
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("project-a", &stubAgent{}, []Platform{p}, filepath.Join(t.TempDir(), "global.json"), LangEnglish)
	e.SetMultiWorkspace(baseDir, filepath.Join(t.TempDir(), "bindings.json"))

	ws1 := e.workspacePool.GetOrCreate(workspace1)
	ws1.agent = &stubAgent{}
	ws1.sessions = NewSessionManager(filepath.Join(t.TempDir(), "w1.json"))
	ws2 := e.workspacePool.GetOrCreate(workspace2)
	ws2.agent = &stubAgent{}
	ws2.sessions = NewSessionManager(filepath.Join(t.TempDir(), "w2.json"))

	channelID := "C-rebind"
	sessionKey := "test:" + channelID + ":user"
	msg := &Message{SessionKey: sessionKey, ChannelID: channelID, Platform: "test"}
	e.workspaceBindings.Bind("project:project-a", workspaceChannelKey("test", channelID), "channel", workspace1)

	_, sessions1, _, _, err := e.commandContextWithWorkspace(p, msg)
	if err != nil {
		t.Fatal(err)
	}
	s1 := sessions1.GetOrCreateActive(sessionKey)
	namespace1, err := SessionNamespace(sessions1)
	if err != nil {
		t.Fatal(err)
	}

	e.workspaceBindings.Bind("project:project-a", workspaceChannelKey("test", channelID), "channel", workspace2)
	_, sessions2, _, _, err := e.commandContextWithWorkspace(p, msg)
	if err != nil {
		t.Fatal(err)
	}
	s2 := sessions2.GetOrCreateActive(sessionKey)
	namespace2, err := SessionNamespace(sessions2)
	if err != nil {
		t.Fatal(err)
	}

	if s1.ID != "s1" || s2.ID != "s1" {
		t.Fatalf("logical IDs = (%q, %q), want independent s1 values", s1.ID, s2.ID)
	}
	if namespace1 == "" || namespace2 == "" || namespace1 == namespace2 {
		t.Fatalf("workspace namespaces = (%q, %q), want distinct non-empty values", namespace1, namespace2)
	}
	if got, err := SessionNamespace(sessions2); err != nil || got != namespace2 {
		t.Fatalf("rebound channel namespace = (%q, %v), want W2 namespace %q", got, err, namespace2)
	}
}
