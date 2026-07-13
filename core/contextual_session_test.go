package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSessionStartContext_NamespaceCanonicalizesStorePath(t *testing.T) {
	realDir := t.TempDir()
	aliasRoot := t.TempDir()
	aliasDir := filepath.Join(aliasRoot, "alias")
	if err := os.Symlink(realDir, aliasDir); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(aliasDir, "sessions.json")
	sessions := NewSessionManager(storePath)
	sessions.GetOrCreateActive("test:user")
	canonicalDir, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}
	canonicalPath := filepath.Join(canonicalDir, "sessions.json")
	sum := sha256.Sum256(append(append([]byte(sessionNamespaceDomain), 0), []byte(canonicalPath)...))
	want := hex.EncodeToString(sum[:])

	got, err := SessionNamespace(sessions)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("SessionNamespace() = %q, want %q", got, want)
	}
	if len(got) != 64 || got != strings.ToLower(got) || strings.Contains(got, realDir) || strings.Contains(got, aliasRoot) {
		t.Fatalf("namespace is not an opaque lowercase 64-character SHA-256: %q", got)
	}
	if got, err := SessionNamespace(NewSessionManager("")); got != "" || !errors.Is(err, errSessionNamespaceUnavailable) {
		t.Fatalf("in-memory manager namespace = (%q, %v), want fail closed", got, err)
	}
}

func invalidUTF8SessionStorePath(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows paths cannot represent an invalid UTF-8 filename through this API")
	}
	return filepath.Join(t.TempDir(), "sessions-"+string([]byte{0xff})+".json")
}

func TestSessionStartContext_InvalidUTF8StorePathFailsClosed(t *testing.T) {
	sessions := NewSessionManager(invalidUTF8SessionStorePath(t))
	sessions.GetOrCreateActive("test:user")
	if got, err := SessionNamespace(sessions); got != "" || !errors.Is(err, errSessionNamespaceUnavailable) {
		t.Fatalf("invalid UTF-8 store namespace = (%q, %v), want fail closed", got, err)
	}

	p := &stubPlatformEngine{n: "test"}
	agent := &contextualFailAgent{}
	e := NewEngine("project-a", agent, []Platform{p}, sessions.StorePath(), LangEnglish)
	e.ReceiveMessage(p, &Message{
		SessionKey: "test:user", Platform: "test", MessageID: "m1", UserID: "user", UserName: "user",
		Content: "must not start", ReplyCtx: "ctx",
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(p.getSent()) != 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	legacy, contextual, _ := agent.snapshot()
	if len(legacy) != 0 || len(contextual) != 0 {
		t.Fatalf("agent started with invalid UTF-8 store path: legacy=%#v contextual=%#v", legacy, contextual)
	}
	if sent := strings.Join(p.getSent(), "\n"); !strings.Contains(strings.ToLower(sent), "failed to start") {
		t.Fatalf("missing fail-closed user response: %q", sent)
	}
}

func TestSessionStartContext_InvalidUTF8CanonicalPathFailsClosed(t *testing.T) {
	canonicalPath := "/canonical/sessions-" + string([]byte{0xfe}) + ".json"
	if got, err := namespaceFromCanonicalSessionStorePath(canonicalPath); got != "" || !errors.Is(err, errSessionNamespaceUnavailable) {
		t.Fatalf("invalid UTF-8 canonical namespace = (%q, %v), want fail closed", got, err)
	}
}

type contextualFailAgent struct {
	mu             sync.Mutex
	legacyCalls    []string
	contextualIDs  []string
	contexts       []SessionStartContext
	contextualErr  error
	contextualSess AgentSession
}

func (a *contextualFailAgent) Name() string { return "contextual" }
func (a *contextualFailAgent) StartSession(_ context.Context, id string) (AgentSession, error) {
	a.mu.Lock()
	a.legacyCalls = append(a.legacyCalls, id)
	a.mu.Unlock()
	return &stubAgentSession{}, nil
}
func (a *contextualFailAgent) StartSessionWithContext(_ context.Context, id string, sessionContext SessionStartContext) (AgentSession, error) {
	a.mu.Lock()
	a.contextualIDs = append(a.contextualIDs, id)
	a.contexts = append(a.contexts, sessionContext)
	err := a.contextualErr
	sess := a.contextualSess
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if sess == nil {
		sess = &stubAgentSession{}
	}
	return sess, nil
}
func (a *contextualFailAgent) ListSessions(context.Context) ([]AgentSessionInfo, error) {
	return nil, nil
}
func (a *contextualFailAgent) Stop() error { return nil }

func (a *contextualFailAgent) snapshot() ([]string, []string, []SessionStartContext) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.legacyCalls...), append([]string(nil), a.contextualIDs...), append([]SessionStartContext(nil), a.contexts...)
}

func TestContextualStart_InteractiveResumeFailureStrictlyPreservesState(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	agent := &contextualFailAgent{contextualErr: errors.New("launcher unavailable")}
	e := NewEngine("project-a", agent, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	e.SetDataDir("/configured/data")
	sessionKey := "test:user"
	session := e.sessions.GetOrCreateActive(sessionKey)
	session.SetAgentSessionID("thread-original", agent.Name())
	session.AddHistory("assistant", "history-before-failure")
	e.sessions.Save()

	e.ReceiveMessage(p, &Message{
		SessionKey: sessionKey, Platform: "test", MessageID: "m1", UserID: "user", UserName: "user",
		Content: "next turn", ReplyCtx: "ctx",
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(p.getSent()) != 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := session.GetAgentSessionID(); got != "thread-original" {
		t.Fatalf("AgentSessionID = %q, want original", got)
	}
	history := session.GetHistory(0)
	if len(history) < 2 || history[0].Content != "history-before-failure" {
		t.Fatalf("history was not preserved: %#v", history)
	}
	legacy, ids, contexts := agent.snapshot()
	if len(legacy) != 0 {
		t.Fatalf("legacy StartSession unexpectedly called: %#v", legacy)
	}
	if len(ids) != 1 || ids[0] != "thread-original" {
		t.Fatalf("contextual resume calls = %#v; fresh fallback must not run", ids)
	}
	if len(contexts) != 1 {
		t.Fatalf("contexts = %#v", contexts)
	}
	ctx := contexts[0]
	wantNamespace, err := SessionNamespace(e.sessions)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Project != "project-a" || ctx.SessionKey != sessionKey || ctx.SessionNamespace != wantNamespace || ctx.LogicalSessionID != session.ID || ctx.AgentType != agent.Name() || ctx.DataDir != "/configured/data" {
		t.Fatalf("context = %#v", ctx)
	}
	if sent := strings.Join(p.getSent(), "\n"); !strings.Contains(strings.ToLower(sent), "failed to start") {
		t.Fatalf("user did not receive start failure: %q", sent)
	}
}

func TestContextualStart_RelayResumeFailureStrictlyPreservesState(t *testing.T) {
	agent := &contextualFailAgent{contextualErr: errors.New("launcher unavailable")}
	e := NewEngine("project-a", agent, nil, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	e.SetDataDir("/configured/data")
	sourceKey := "test:chat:user"
	relayKey := relayConversationKey("source", "test", "chat")
	session := e.sessions.GetOrCreateActive(relayKey)
	session.SetAgentSessionID("relay-thread-original", agent.Name())
	session.AddHistory("assistant", "relay-history")
	e.sessions.Save()

	if _, err := e.HandleRelay(context.Background(), "source", sourceKey, "next relay turn"); err == nil {
		t.Fatal("HandleRelay unexpectedly succeeded")
	}
	if got := session.GetAgentSessionID(); got != "relay-thread-original" {
		t.Fatalf("relay AgentSessionID = %q", got)
	}
	if history := session.GetHistory(0); len(history) != 1 || history[0].Content != "relay-history" {
		t.Fatalf("relay history changed: %#v", history)
	}
	legacy, ids, contexts := agent.snapshot()
	if len(legacy) != 0 || len(ids) != 1 || ids[0] != "relay-thread-original" {
		t.Fatalf("legacy=%#v contextual=%#v", legacy, ids)
	}
	if len(contexts) != 1 || contexts[0].LogicalSessionID != session.ID || contexts[0].SessionKey != sourceKey {
		t.Fatalf("relay context = %#v", contexts)
	}
}

func TestContextualStart_WorkspaceS1AndRebindUseDistinctNamespace(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("project-a", &contextualFailAgent{}, []Platform{p}, filepath.Join(t.TempDir(), "global.json"), LangEnglish)
	e.SetMultiWorkspace(t.TempDir(), filepath.Join(t.TempDir(), "bindings.json"))

	workspace1 := normalizeWorkspacePath(t.TempDir())
	workspace2 := normalizeWorkspacePath(t.TempDir())
	agent1 := &contextualFailAgent{}
	agent2 := &contextualFailAgent{}
	ws1 := e.workspacePool.GetOrCreate(workspace1)
	ws1.agent = agent1
	ws1.sessions = NewSessionManager(filepath.Join(t.TempDir(), "w1.json"))
	ws2 := e.workspacePool.GetOrCreate(workspace2)
	ws2.agent = agent2
	ws2.sessions = NewSessionManager(filepath.Join(t.TempDir(), "w2.json"))

	channelID := "C-context-rebind"
	sessionKey := "test:" + channelID + ":user"
	msg := &Message{SessionKey: sessionKey, ChannelID: channelID, Platform: "test"}
	e.workspaceBindings.Bind("project:project-a", workspaceChannelKey("test", channelID), "channel", workspace1)

	resolvedAgent1, sessions1, interactiveKey1, _, err := e.commandContextWithWorkspace(p, msg)
	if err != nil {
		t.Fatal(err)
	}
	s1 := sessions1.GetOrCreateActive(sessionKey)
	state1 := e.getOrCreateInteractiveStateWith(interactiveKey1, p, "ctx1", s1, sessions1, resolvedAgent1, sessionKey)
	if state1.agentSession == nil {
		t.Fatal("W1 contextual session did not start")
	}

	e.workspaceBindings.Bind("project:project-a", workspaceChannelKey("test", channelID), "channel", workspace2)
	resolvedAgent2, sessions2, interactiveKey2, _, err := e.commandContextWithWorkspace(p, msg)
	if err != nil {
		t.Fatal(err)
	}
	s2 := sessions2.GetOrCreateActive(sessionKey)
	state2 := e.getOrCreateInteractiveStateWith(interactiveKey2, p, "ctx2", s2, sessions2, resolvedAgent2, sessionKey)
	if state2.agentSession == nil {
		t.Fatal("W2 contextual session did not start")
	}

	_, _, contexts1 := agent1.snapshot()
	_, _, contexts2 := agent2.snapshot()
	if s1.ID != "s1" || s2.ID != "s1" || len(contexts1) != 1 || len(contexts2) != 1 {
		t.Fatalf("workspace contexts: ids=(%q,%q) contexts=(%#v,%#v)", s1.ID, s2.ID, contexts1, contexts2)
	}
	if contexts1[0].SessionNamespace == "" || contexts2[0].SessionNamespace == "" || contexts1[0].SessionNamespace == contexts2[0].SessionNamespace {
		t.Fatalf("rebind reused namespace: W1=%q W2=%q", contexts1[0].SessionNamespace, contexts2[0].SessionNamespace)
	}
	want2, err := SessionNamespace(sessions2)
	if err != nil || contexts2[0].SessionNamespace != want2 {
		t.Fatalf("W2 context namespace=(%q,%v), want %q", contexts2[0].SessionNamespace, err, want2)
	}
}

func TestSessionStartContext_EnvironmentIsMinimalAndFresh(t *testing.T) {
	ctx := SessionStartContext{Project: "p", SessionKey: "k", SessionNamespace: strings.Repeat("a", 64), LogicalSessionID: "s1", AgentType: "codex", DataDir: "/data"}
	env1 := ctx.Environment()
	env2 := ctx.Environment()
	env1[0] = "MUTATED=yes"
	joined := strings.Join(env2, "\n")
	for _, want := range []string{"CC_PROJECT=p", "CC_SESSION_KEY=k", "CC_SESSION_NAMESPACE=" + strings.Repeat("a", 64), "CC_LOGICAL_SESSION_ID=s1", "CC_AGENT_TYPE=codex", "CC_DATA_DIR=/data"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %#v", want, env2)
		}
	}
	if len(env2) != 6 || strings.Contains(joined, "MUTATED") {
		t.Fatalf("environment is not minimal/fresh: %#v", env2)
	}
}

func TestSessionStartContext_MissingPersistentStoreFailsClosed(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	agent := &contextualFailAgent{}
	e := NewEngine("project-a", agent, []Platform{p}, "", LangEnglish)
	e.ReceiveMessage(p, &Message{
		SessionKey: "test:user", Platform: "test", MessageID: "m1", UserID: "user", UserName: "user",
		Content: "must not start", ReplyCtx: "ctx",
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(p.getSent()) != 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	legacy, contextual, _ := agent.snapshot()
	if len(legacy) != 0 || len(contextual) != 0 {
		t.Fatalf("agent started without stable persistent namespace: legacy=%#v contextual=%#v", legacy, contextual)
	}
	if sent := strings.Join(p.getSent(), "\n"); !strings.Contains(strings.ToLower(sent), "failed to start") {
		t.Fatalf("missing fail-closed user response: %q", sent)
	}
}

type conditionalLegacyAgent struct {
	mu              sync.Mutex
	contextualCalls int
	legacyIDs       []string
}

func (a *conditionalLegacyAgent) Name() string { return "conditional-legacy" }
func (a *conditionalLegacyAgent) StartSession(_ context.Context, id string) (AgentSession, error) {
	a.mu.Lock()
	a.legacyIDs = append(a.legacyIDs, id)
	a.mu.Unlock()
	if id != "" {
		return nil, errors.New("legacy resume failed")
	}
	return newResultAgentSession("legacy-fresh-recovered"), nil
}
func (a *conditionalLegacyAgent) StartSessionWithContext(context.Context, string, SessionStartContext) (AgentSession, error) {
	a.mu.Lock()
	a.contextualCalls++
	a.mu.Unlock()
	return nil, ErrContextualStartUnsupported
}
func (a *conditionalLegacyAgent) ListSessions(context.Context) ([]AgentSessionInfo, error) {
	return nil, nil
}
func (a *conditionalLegacyAgent) Stop() error { return nil }

func TestContextualStart_UnsupportedBackendRetainsLegacyResumeFallback(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	agent := &conditionalLegacyAgent{}
	e := NewEngine("project-a", agent, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	sessionKey := "test:user"
	session := e.sessions.GetOrCreateActive(sessionKey)
	session.SetAgentSessionID("stale-app-server-id", agent.Name())
	e.sessions.Save()

	e.ReceiveMessage(p, &Message{
		SessionKey: sessionKey, Platform: "test", MessageID: "m1", UserID: "user", UserName: "user",
		Content: "legacy fallback turn", ReplyCtx: "ctx",
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(p.getSent(), "\n"), "legacy-fresh-recovered") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	agent.mu.Lock()
	contextualCalls := agent.contextualCalls
	legacyIDs := append([]string(nil), agent.legacyIDs...)
	agent.mu.Unlock()
	if contextualCalls != 2 {
		t.Fatalf("contextual attempts = %d, want resume + fresh capability probes", contextualCalls)
	}
	if len(legacyIDs) != 2 || legacyIDs[0] != "stale-app-server-id" || legacyIDs[1] != "" {
		t.Fatalf("legacy StartSession calls = %#v", legacyIDs)
	}
	if sent := strings.Join(p.getSent(), "\n"); !strings.Contains(sent, "legacy-fresh-recovered") {
		t.Fatalf("legacy fallback response missing: %q", sent)
	}
}
