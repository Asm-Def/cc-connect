package core

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	directCommandTimeout    = 60 * time.Second
	directCommandMaxCapture = 16 * 1024
	directCommandMaxDisplay = 4000
	directCommandMaxArgs    = 8
	directCommandMaxArgLen  = 128
)

// rawCommandSuffix returns everything after the slash-command name without
// interpreting quoting or escapes. Direct mode must parse this original text,
// not the legacy splitCommandArgs result (which intentionally accepts unmatched
// quotes for compatibility).
func rawCommandSuffix(raw string) string {
	for i := 0; i < len(raw); i++ {
		if raw[i] == ' ' || raw[i] == '\t' {
			return strings.TrimLeft(raw[i:], " \t")
		}
	}
	return ""
}

// tokenizeDirectCommand implements the deliberately small direct-command
// grammar: ASCII space/tab separators, no quoting or escaping, and bounded
// UTF-8 tokens. Accepted bytes are returned one-to-one as argv elements.
func tokenizeDirectCommand(raw string) ([]string, error) {
	if !utf8.ValidString(raw) {
		return nil, fmt.Errorf("arguments must be valid UTF-8")
	}

	var tokens []string
	start := -1
	finish := func(end int) error {
		if start < 0 {
			return nil
		}
		if end-start > directCommandMaxArgLen {
			return fmt.Errorf("argument exceeds %d bytes", directCommandMaxArgLen)
		}
		tokens = append(tokens, raw[start:end])
		if len(tokens) > directCommandMaxArgs {
			return fmt.Errorf("too many arguments (maximum %d)", directCommandMaxArgs)
		}
		start = -1
		return nil
	}

	for i, r := range raw {
		if r == ' ' || r == '\t' {
			if err := finish(i); err != nil {
				return nil, err
			}
			continue
		}
		if r == '\'' || r == '"' || r == '\\' {
			return nil, fmt.Errorf("quotes and escapes are not supported")
		}
		if unicode.IsControl(r) {
			return nil, fmt.Errorf("control characters are not allowed")
		}
		if start < 0 {
			start = i
		}
	}
	if err := finish(len(raw)); err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("at least one argument is required")
	}
	return tokens, nil
}

var directExecMetacharacters = strings.NewReplacer(
	";", "", "&", "", "|", "", "<", "", ">", "", "`", "", "$", "",
	"*", "", "?", "", "'", "", "\"", "", "\\", "", "\n", "", "\r", "", "\x00", "",
)

func containsDirectExecMetacharacter(path string) bool {
	return directExecMetacharacters.Replace(path) != path
}

// validateDirectExecutable rejects shell-like command strings and paths that
// another local user can replace. Symlinks are rejected at every component by
// requiring the fully evaluated path to equal the configured path.
func validateDirectExecutable(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("executable must be an absolute path")
	}
	if containsDirectExecMetacharacter(path) {
		return fmt.Errorf("executable path contains forbidden metacharacters")
	}
	clean := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	if filepath.Clean(resolved) != clean {
		return fmt.Errorf("executable path must not contain symlinks")
	}
	info, err := os.Stat(clean)
	if err != nil {
		return fmt.Errorf("stat executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("executable must be a regular file")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("executable is not executable")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("executable must not be group- or world-writable")
	}
	if err := validateDirectExecutableOwner(info, "executable"); err != nil {
		return err
	}

	for dir := filepath.Dir(clean); ; dir = filepath.Dir(dir) {
		dirInfo, err := os.Stat(dir)
		if err != nil {
			return fmt.Errorf("stat executable parent %q: %w", dir, err)
		}
		if !dirInfo.IsDir() {
			return fmt.Errorf("executable parent %q is not a directory", dir)
		}
		if dirInfo.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("executable parent %q is group- or world-writable", dir)
		}
		if err := validateDirectExecutableOwner(dirInfo, "executable parent"); err != nil {
			return err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return nil
}

func validateDirectOwnerID(ownerID, effectiveUserID uint32, subject string) error {
	if ownerID != effectiveUserID && ownerID != 0 {
		return fmt.Errorf("%s must be owned by the current user or root", subject)
	}
	return nil
}

type cappedDirectOutput struct {
	mu        sync.Mutex
	buf       strings.Builder
	remaining int
	truncated bool
}

func newCappedDirectOutput(limit int) *cappedDirectOutput {
	return &cappedDirectOutput{remaining: limit}
}

func (w *cappedDirectOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(p) > w.remaining {
		if w.remaining > 0 {
			w.buf.Write(p[:w.remaining])
		}
		w.remaining = 0
		w.truncated = true
		return len(p), nil
	}
	w.buf.Write(p)
	w.remaining -= len(p)
	return len(p), nil
}

func (w *cappedDirectOutput) result() (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.ToValidUTF8(w.buf.String(), "�"), w.truncated
}

type directProcessResult struct {
	Output    string
	Truncated bool
	ExitCode  int
	TimedOut  bool
	WaitErr   error
}

func runDirectExecutable(ctx context.Context, executable string, args []string, workDir string, env []string) directProcessResult {
	output := newCappedDirectOutput(directCommandMaxCapture)
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = workDir
	// Direct commands receive only the documented call-local context. In
	// particular, daemon API keys and OAuth variables are not inherited.
	cmd.Env = append([]string(nil), env...)
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()
	text, truncated := output.result()
	result := directProcessResult{Output: text, Truncated: truncated, ExitCode: -1, WaitErr: err}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	result.TimedOut = ctx.Err() == context.DeadlineExceeded
	return result
}

var (
	directSensitiveAssignment = regexp.MustCompile(`(?i)\b(api[_-]?key|token|secret|password|credential)(\s*[:=]\s*)([^\s]+)`)
	directBearerValue         = regexp.MustCompile(`(?i)\bbearer\s+[^\s]+`)
)

func redactDirectCommandOutput(output string) string {
	output = directSensitiveAssignment.ReplaceAllString(output, `${1}${2}[REDACTED]`)
	return directBearerValue.ReplaceAllString(output, "Bearer [REDACTED]")
}

type ordinaryAdmission int

const (
	ordinaryAdmissionBusy ordinaryAdmission = iota
	ordinaryAdmissionAcquired
	ordinaryAdmissionExclusive
)

// tryAdmitOrdinaryMessage and admitExclusiveCommand share the same lock order:
// interactiveMu -> state.mu -> session.mu. This makes the decision between an
// ordinary turn and a session-exclusive command atomic without changing the
// persisted Session schema.
func (e *Engine) tryAdmitOrdinaryMessage(interactiveKey string, session *Session) ordinaryAdmission {
	e.interactiveMu.Lock()
	if e.exclusiveCommands[interactiveKey] != nil {
		e.interactiveMu.Unlock()
		return ordinaryAdmissionExclusive
	}
	state := e.interactiveStates[interactiveKey]
	if state == nil {
		state = &interactiveState{eventsNeedResync: true}
		e.interactiveStates[interactiveKey] = state
	}
	state.mu.Lock()
	e.interactiveMu.Unlock()
	defer state.mu.Unlock()
	if state.exclusiveCommand {
		return ordinaryAdmissionExclusive
	}
	if !session.TryLock() {
		return ordinaryAdmissionBusy
	}
	return ordinaryAdmissionAcquired
}

func (e *Engine) admitExclusiveCommand(interactiveKey string, session *Session, p Platform, replyCtx any) (*interactiveState, bool) {
	e.interactiveMu.Lock()
	defer e.interactiveMu.Unlock()
	if e.exclusiveCommands == nil {
		e.exclusiveCommands = make(map[string]*interactiveState)
	}
	if e.exclusiveCommands[interactiveKey] != nil {
		return nil, false
	}
	state := e.interactiveStates[interactiveKey]
	if state == nil {
		state = &interactiveState{platform: p, replyCtx: replyCtx, eventsNeedResync: true}
		e.interactiveStates[interactiveKey] = state
	}
	state.mu.Lock()
	defer state.mu.Unlock()

	if state.exclusiveCommand || len(state.pendingMessages) != 0 || !session.TryLock() {
		return nil, false
	}
	state.exclusiveCommand = true
	e.exclusiveCommands[interactiveKey] = state
	return state, true
}

func (e *Engine) releaseExclusiveCommand(interactiveKey string, state *interactiveState, session *Session) {
	e.interactiveMu.Lock()
	state.mu.Lock()
	// Keep state.mu held across unlock + sentinel clear so a waiting ordinary
	// admission cannot observe a half-released state.
	session.UnlockWithoutUpdate()
	state.exclusiveCommand = false
	if e.exclusiveCommands[interactiveKey] == state {
		delete(e.exclusiveCommands, interactiveKey)
	}
	state.mu.Unlock()
	e.interactiveMu.Unlock()
}

type directCommandContext struct {
	project          string
	sessionKey       string
	sessionNamespace string
	logicalSessionID string
	agentType        string
	dataDir          string
}

func (c directCommandContext) env() []string {
	env := []string{
		"CC_PROJECT=" + c.project,
		"CC_SESSION_KEY=" + c.sessionKey,
		"CC_SESSION_NAMESPACE=" + c.sessionNamespace,
		"CC_LOGICAL_SESSION_ID=" + c.logicalSessionID,
		"CC_AGENT_TYPE=" + c.agentType,
	}
	if c.dataDir != "" {
		env = append(env, "CC_DATA_DIR="+c.dataDir)
	}
	return env
}

func (e *Engine) executeDirectCommand(p Platform, msg *Message, command *CustomCommand, rawSuffix string) {
	args, err := tokenizeDirectCommand(rawSuffix)
	if err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCommandExecError), command.Name, err))
		return
	}
	if err := validateDirectExecutable(command.Exec); err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCommandExecError), command.Name, err))
		return
	}

	agent, sessions, interactiveKey, workspaceDir, err := e.commandContextWithWorkspace(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
		return
	}
	workDir := command.WorkDir
	if workDir == "" {
		workDir = workspaceDir
	}
	if workDir == "" {
		if workDirAgent, ok := agent.(interface{ GetWorkDir() string }); ok {
			workDir = workDirAgent.GetWorkDir()
		}
	}
	if workDir == "" {
		workDir, _ = os.Getwd()
	}

	var (
		state   *interactiveState
		session *Session
		env     []string
	)
	if command.SessionExclusive {
		// The workspace-local SessionManager is keyed by the original platform
		// session key. interactiveKey is only the runtime state-map key.
		session = sessions.GetOrCreateActive(msg.SessionKey)
		var admitted bool
		state, admitted = e.admitExclusiveCommand(interactiveKey, session, p, msg.ReplyCtx)
		if !admitted {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSessionExclusiveBusy))
			return
		}
		sessionNamespace, err := SessionNamespace(sessions)
		if err != nil {
			e.releaseExclusiveCommand(interactiveKey, state, session)
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCommandExecError), command.Name, err))
			return
		}
		context := directCommandContext{
			project: e.name, sessionKey: msg.SessionKey, sessionNamespace: sessionNamespace, logicalSessionID: session.ID,
			agentType: agent.Name(), dataDir: e.dataDir,
		}
		env = context.env()
	}

	slog.Info("executing direct command", "command", command.Name, "executable", command.Exec, "user", msg.UserName)
	go func() {
		if state != nil {
			defer e.releaseExclusiveCommand(interactiveKey, state, session)
		}
		ctx, cancel := context.WithTimeout(e.ctx, directCommandTimeout)
		defer cancel()
		result := runDirectExecutable(ctx, command.Exec, args, workDir, env)
		output := strings.TrimSpace(redactDirectCommandOutput(result.Output))
		if result.Truncated {
			output = truncateRunes(output, directCommandMaxDisplay) + "\n" + e.i18n.T(MsgDirectCommandOutputTruncated)
		} else {
			output = truncateRunes(output, directCommandMaxDisplay)
		}
		if result.TimedOut {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCommandExecTimeout), command.Name))
			return
		}
		if result.WaitErr != nil {
			status := result.WaitErr.Error()
			if result.ExitCode >= 0 {
				status = fmt.Sprintf("exit code %d", result.ExitCode)
			}
			if output != "" {
				status += "\n" + output
			}
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgCommandExecError), command.Name, status))
			return
		}
		if output == "" {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgCommandExecSuccess))
			return
		}
		e.reply(p, msg.ReplyCtx, output)
	}()
}
