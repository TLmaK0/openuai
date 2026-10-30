package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"openuai/internal/agent"
	"openuai/internal/llm"
)

func TestBackgroundRunFlag(t *testing.T) {
	if inBackgroundRun(context.Background()) {
		t.Fatal("plain context is not a background run")
	}
	if !inBackgroundRun(context.WithValue(context.Background(), backgroundRunKey{}, true)) {
		t.Fatal("flag not read")
	}
}

func TestBgStoreSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s := loadBgStore(dir)
	e := bgSubAgent{AgentID: "a1", TaskID: "long", Description: "sleep", Model: "sonnet", Cwd: "/p", Parent: "main", StartedAt: time.Now()}
	s.put(e)
	s.put(bgSubAgent{AgentID: "a2", TaskID: "short"})
	s.remove("a2")
	got := loadBgStore(dir).list()
	if len(got) != 1 || got[0].AgentID != "a1" || got[0].Model != "sonnet" || got[0].Cwd != "/p" || !got[0].StartedAt.Equal(e.StartedAt) {
		t.Fatalf("got %+v", got)
	}
	if !s.has("a1") || s.has("a2") {
		t.Fatal("has")
	}
	s.remove("a1")
	if len(loadBgStore(dir).list()) != 0 {
		t.Fatal("not removed")
	}
}

func TestBgKeyIsStableAndCarriesTaskID(t *testing.T) {
	e := bgSubAgent{AgentID: "a1", TaskID: "my:task", StartedAt: time.Unix(10, 5)}
	e2 := e
	e2.Restarts = 2
	if e.key() != e2.key() {
		t.Fatal("key must not change across restarts")
	}
	if got := taskIDFromKey(e.key()); got != "my:task" {
		t.Fatalf("task id %q", got)
	}
	e3 := e
	e3.StartedAt = time.Unix(11, 0)
	if e3.key() == e.key() {
		t.Fatal("a new launch of the same agent_id needs a new key")
	}
}

func TestPendingHasAndKeys(t *testing.T) {
	s := loadPendingStore(t.TempDir())
	s.Add("subagent:a:1:x", "n")
	s.Add("wa:1", "n")
	s.Add("subagent:b:1:y", "n")
	s.Done("subagent:b:1:y")
	if !s.Has("subagent:a:1:x") || !s.Has("subagent:b:1:y") || s.Has("subagent:c") {
		t.Fatal("Has")
	}
	if k := s.Keys("subagent:"); len(k) != 1 || k[0] != "subagent:a:1:x" {
		t.Fatalf("Keys %v", k)
	}
}

func saveTr(t *testing.T, dir, id, status string, msgs ...llm.Message) {
	t.Helper()
	if err := agent.SaveTranscript(dir, agent.Transcript{ID: id, Status: status, Messages: msgs}); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverBackground(t *testing.T) {
	dir := t.TempDir()
	user := llm.Message{Role: llm.RoleUser, Content: "task"}
	saveTr(t, dir, "running", agent.StatusRunning, user)
	saveTr(t, dir, "interrupted", agent.StatusInterrupted, user)
	saveTr(t, dir, "done", agent.StatusFinished, user, llm.Message{Role: llm.RoleAssistant, Content: "SHORT90"})
	saveTr(t, dir, "tired", agent.StatusRunning, user)
	now := time.Now()
	entries := []bgSubAgent{
		{AgentID: "running", TaskID: "t-run", StartedAt: now},
		{AgentID: "interrupted", TaskID: "t-int", StartedAt: now, Restarts: 1},
		{AgentID: "done", TaskID: "t-done", StartedAt: now},
		{AgentID: "gone", TaskID: "t-gone", StartedAt: now},
		{AgentID: "tired", TaskID: "t-tired", StartedAt: now, Restarts: maxBgRestarts - 1},
		{AgentID: "delivered", TaskID: "t-del", StartedAt: now},
	}
	deliveredKey := entries[5].key()
	r := recoverBackground(entries, dir, func(k string) bool { return k == deliveredKey })

	if len(r.resume) != 2 || r.resume[0].AgentID != "running" || r.resume[0].Restarts != 1 || r.resume[1].Restarts != 2 {
		t.Fatalf("resume %+v", r.resume)
	}
	byID := map[string]string{}
	for _, q := range r.queued {
		byID[q.e.AgentID] = q.notification
	}
	if len(byID) != 4 {
		t.Fatalf("queued %v", byID)
	}
	if n := byID["done"]; !strings.Contains(n, "SHORT90") || !strings.HasPrefix(n, bgNotificationPrefix+"t-done]") {
		t.Fatalf("finished result not delivered: %q", n)
	}
	if n := byID["gone"]; !strings.Contains(n, "Error: failed:") || !strings.Contains(n, "cannot be used to resume") || strings.Contains(n, "agent_id: gone") {
		t.Fatalf("missing transcript not reported: %q", n)
	}
	if n := byID["tired"]; !strings.Contains(n, "Error: failed:") || !strings.Contains(n, "restarts") {
		t.Fatalf("too many restarts not reported: %q", n)
	}
	if byID["delivered"] != "" {
		t.Fatal("an already delivered result must not be queued again")
	}
	if len(r.failed) != 2 || !r.failedKeys[entries[3].key()] {
		t.Fatalf("failed %v", r.failed)
	}

	note := bgRestartNote(r, []string{"t-done"})
	for _, want := range []string{"t-run (agent_id running)", "do NOT launch them again", "already finished", "t-done", "could not be resumed", "t-gone"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note lacks %q: %s", want, note)
		}
	}
	if bgRestartNote(bgRecovery{}, nil) != "" {
		t.Fatal("empty note expected")
	}
}

func TestBgResumeMessageNotesRestart(t *testing.T) {
	if m := bgResumeMessage(time.Now()); !strings.HasPrefix(m, agent.RestartNotePrefix) || !strings.Contains(m, "restarted") {
		t.Fatal(m)
	}
}
