package agent

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestSanitizeHistoryForProvider_KeepsMultipleToolResultsSameTurn(t *testing.T) {
	history := []providers.Message{
		{Role: "user", Content: "do task"},
		{
			Role: "assistant",
			ToolCalls: []providers.ToolCall{
				{ID: "call-1", Name: "tool_a"},
				{ID: "call-2", Name: "tool_b"},
			},
		},
		{Role: "tool", ToolCallID: "call-1", Content: "result a"},
		{Role: "tool", ToolCallID: "call-2", Content: "result b"},
		{Role: "assistant", Content: "done"},
	}

	got := sanitizeHistoryForProvider(history)
	if len(got) != len(history) {
		t.Fatalf("expected %d messages, got %d", len(history), len(got))
	}
	if got[2].Role != "tool" || got[2].ToolCallID != "call-1" {
		t.Fatalf("expected tool result for call-1 at index 2, got role=%q id=%q", got[2].Role, got[2].ToolCallID)
	}
	if got[3].Role != "tool" || got[3].ToolCallID != "call-2" {
		t.Fatalf("expected tool result for call-2 at index 3, got role=%q id=%q", got[3].Role, got[3].ToolCallID)
	}
}

func TestSanitizeHistoryForProvider_DropsUnknownToolCallID(t *testing.T) {
	history := []providers.Message{
		{Role: "user", Content: "do task"},
		{
			Role: "assistant",
			ToolCalls: []providers.ToolCall{
				{ID: "call-1", Name: "tool_a"},
			},
		},
		{Role: "tool", ToolCallID: "unknown", Content: "bad result"},
		{Role: "assistant", Content: "done"},
	}

	got := sanitizeHistoryForProvider(history)
	if len(got) != 3 {
		t.Fatalf("expected 3 messages after dropping unknown tool_call_id, got %d", len(got))
	}
	if got[2].Role != "assistant" || got[2].Content != "done" {
		t.Fatalf("expected trailing assistant message preserved, got role=%q content=%q", got[2].Role, got[2].Content)
	}
}
