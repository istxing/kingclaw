package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"telegram:123456", "telegram_123456"},
		{"discord:987654321", "discord_987654321"},
		{"slack:C01234", "slack_C01234"},
		{"no-colons-here", "no-colons-here"},
		{"multiple:colons:here", "multiple_colons_here"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeFilename(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSave_WithColonInKey(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSessionManager(tmpDir)

	// Create a session with a key containing colon (typical channel session key).
	key := "telegram:123456"
	sm.GetOrCreate(key)
	sm.AddMessage(key, "user", "hello")
	sm.SetThinkingLevel(key, "medium")
	sm.SetVerboseLevel(key, "full")

	// Save should succeed even though the key contains ':'
	if err := sm.Save(key); err != nil {
		t.Fatalf("Save(%q) failed: %v", key, err)
	}

	// The file on disk should use sanitized name.
	expectedFile := filepath.Join(tmpDir, "telegram_123456.json")
	if _, err := os.Stat(expectedFile); os.IsNotExist(err) {
		t.Fatalf("expected session file %s to exist", expectedFile)
	}

	// Load into a fresh manager and verify the session round-trips.
	sm2 := NewSessionManager(tmpDir)
	history := sm2.GetHistory(key)
	if len(history) != 1 {
		t.Fatalf("expected 1 message after reload, got %d", len(history))
	}
	if history[0].Content != "hello" {
		t.Errorf("expected message content %q, got %q", "hello", history[0].Content)
	}
	if got := sm2.GetThinkingLevel(key); got != "medium" {
		t.Errorf("thinking level = %q, want %q", got, "medium")
	}
	if got := sm2.GetVerboseLevel(key); got != "full" {
		t.Errorf("verbose level = %q, want %q", got, "full")
	}
}

func TestSessionOverrides_SetAndGet(t *testing.T) {
	sm := NewSessionManager("")
	key := "cli:default"

	if got := sm.GetThinkingLevel(key); got != "" {
		t.Fatalf("initial thinking = %q, want empty", got)
	}
	if got := sm.GetVerboseLevel(key); got != "" {
		t.Fatalf("initial verbose = %q, want empty", got)
	}

	sm.SetThinkingLevel(key, "low")
	sm.SetVerboseLevel(key, "on")

	if got := sm.GetThinkingLevel(key); got != "low" {
		t.Fatalf("thinking = %q, want low", got)
	}
	if got := sm.GetVerboseLevel(key); got != "on" {
		t.Fatalf("verbose = %q, want on", got)
	}
}

func TestSave_RejectsPathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSessionManager(tmpDir)

	badKeys := []string{"", ".", "..", "foo/bar", "foo\\bar"}
	for _, key := range badKeys {
		sm.GetOrCreate(key)
		if err := sm.Save(key); err == nil {
			t.Errorf("Save(%q) should have failed but didn't", key)
		}
	}
}
