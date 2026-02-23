package runs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestService_ListIncludesCustomAndCron(t *testing.T) {
	tmp := t.TempDir()
	svc := NewService(tmp)

	if err := svc.Append(Entry{
		RunID:      "exec-1",
		Status:     "completed",
		TS:         2000,
		DurationMS: 12,
		Source:     "exec",
	}); err != nil {
		t.Fatalf("append custom: %v", err)
	}

	cronPath := filepath.Join(tmp, "cron", "runs", "job-a.jsonl")
	if err := os.MkdirAll(filepath.Dir(cronPath), 0o700); err != nil {
		t.Fatalf("mkdir cron dir: %v", err)
	}

	content := `{"ts":3000,"jobId":"job-a","runId":"cron-1","status":"ok","durationMs":50}
{"ts":1000,"jobId":"job-a","runId":"cron-2","status":"error","error":"boom","durationMs":5}
`
	if err := os.WriteFile(cronPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write cron file: %v", err)
	}

	entries, err := svc.List(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].RunID != "cron-1" || entries[0].Status != "completed" {
		t.Fatalf("unexpected first entry: %#v", entries[0])
	}
	if entries[1].RunID != "exec-1" {
		t.Fatalf("unexpected second entry: %#v", entries[1])
	}
}

func TestService_GetAndErrors(t *testing.T) {
	tmp := t.TempDir()
	svc := NewService(tmp)

	_ = svc.Append(Entry{RunID: "ok-1", Status: "completed", TS: 10, Source: "exec"})
	_ = svc.Append(Entry{RunID: "bad-1", Status: "failed", TS: 20, Error: "x", Source: "message"})

	got, found, err := svc.Get("bad-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !found || got == nil || got.RunID != "bad-1" {
		t.Fatalf("expected to find bad-1, got found=%v entry=%#v", found, got)
	}

	errs, err := svc.Errors(10)
	if err != nil {
		t.Fatalf("errors: %v", err)
	}
	if len(errs) != 1 || errs[0].RunID != "bad-1" {
		t.Fatalf("unexpected errors: %#v", errs)
	}
}

func TestService_GetPreservesAgentLLMFields(t *testing.T) {
	tmp := t.TempDir()
	svc := NewService(tmp)

	err := svc.Append(Entry{
		RunID:      "agent-1",
		Status:     "completed",
		TS:         30,
		Source:     "agent_llm",
		Model:      "openai/gpt-5.2-codex",
		Attempts:   "#1 openai/gpt-5.2-codex status=failed reason=rate_limit duration=120ms error=429",
		Thinking:   "high",
		Verbose:    "low",
		DurationMS: 1234,
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	got, found, err := svc.Get("agent-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !found || got == nil {
		t.Fatalf("expected to find agent-1")
	}
	if got.Model != "openai/gpt-5.2-codex" {
		t.Fatalf("unexpected model: %q", got.Model)
	}
	if got.Thinking != "high" || got.Verbose != "low" {
		t.Fatalf("unexpected thinking/verbose: %q/%q", got.Thinking, got.Verbose)
	}
	if got.Attempts == "" {
		t.Fatalf("expected fallback attempts to be preserved")
	}
}
