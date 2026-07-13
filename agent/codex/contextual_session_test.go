package codex

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func lastEnvValue(env []string, key string) (string, int) {
	prefix := key + "="
	value := ""
	count := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			value = strings.TrimPrefix(entry, prefix)
			count++
		}
	}
	return value, count
}

func TestStartSessionWithContext_ConcurrentCallsDoNotCrossContaminate(t *testing.T) {
	agent := &Agent{
		workDir: ".", backend: "exec", cmd: "codex", activeIdx: -1,
		sessionEnv: []string{
			"CC_PROJECT=stale", "CC_SESSION_KEY=stale", "CC_SESSION_NAMESPACE=stale", "CC_LOGICAL_SESSION_ID=stale", "CC_AGENT_TYPE=stale",
		},
	}

	const count = 64
	errCh := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			wantID := fmt.Sprintf("logical-%d", i)
			wantKey := fmt.Sprintf("session-%d", i)
			wantNamespace := fmt.Sprintf("%064x", i+1)
			session, err := agent.StartSessionWithContext(context.Background(), "thread-1", core.SessionStartContext{
				Project: "project-a", SessionKey: wantKey, SessionNamespace: wantNamespace, LogicalSessionID: wantID, AgentType: "codex", DataDir: "/data",
			})
			if err != nil {
				errCh <- err
				return
			}
			cs, ok := session.(*codexSession)
			if !ok {
				errCh <- fmt.Errorf("session type = %T", session)
				return
			}
			defer cs.Close()
			checks := map[string]string{
				"CC_PROJECT": "project-a", "CC_SESSION_KEY": wantKey, "CC_SESSION_NAMESPACE": wantNamespace, "CC_LOGICAL_SESSION_ID": wantID,
				"CC_AGENT_TYPE": "codex", "CC_DATA_DIR": "/data",
			}
			for key, want := range checks {
				got, occurrences := lastEnvValue(cs.extraEnv, key)
				if got != want || occurrences != 1 {
					errCh <- fmt.Errorf("%s=(%q,count=%d), want (%q,1); env=%#v", key, got, occurrences, want, core.RedactEnv(cs.extraEnv))
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestStartSession_LegacyPathRetainsSessionEnv(t *testing.T) {
	agent := &Agent{workDir: ".", backend: "exec", cmd: "codex", activeIdx: -1, sessionEnv: []string{"CC_SESSION_KEY=legacy"}}
	session, err := agent.StartSession(context.Background(), "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	cs := session.(*codexSession)
	defer cs.Close()
	if got, count := lastEnvValue(cs.extraEnv, "CC_SESSION_KEY"); got != "legacy" || count != 1 {
		t.Fatalf("legacy CC_SESSION_KEY=(%q,%d), env=%#v", got, count, cs.extraEnv)
	}
}

func TestStartSessionWithContext_AppServerDeclinesOptionalCapability(t *testing.T) {
	agent := &Agent{backend: "app_server"}
	session, err := agent.StartSessionWithContext(context.Background(), "thread-1", core.SessionStartContext{
		Project: "p", SessionKey: "k", LogicalSessionID: "s1", AgentType: "codex",
	})
	if session != nil || !errors.Is(err, core.ErrContextualStartUnsupported) {
		t.Fatalf("session=%T err=%v, want ErrContextualStartUnsupported", session, err)
	}
}

func TestStartSessionWithContext_ExecRejectsMissingNamespace(t *testing.T) {
	agent := &Agent{workDir: ".", backend: "exec", cmd: "codex", activeIdx: -1}
	session, err := agent.StartSessionWithContext(context.Background(), "thread-1", core.SessionStartContext{
		Project: "p", SessionKey: "k", LogicalSessionID: "s1", AgentType: "codex",
	})
	if session != nil || err == nil || !strings.Contains(err.Error(), "session namespace") {
		t.Fatalf("session=%T err=%v, want namespace failure", session, err)
	}
}
