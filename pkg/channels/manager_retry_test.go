package channels

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/istxing/kingclaw/pkg/bus"
	"github.com/istxing/kingclaw/pkg/config"
)

type flakyChannel struct {
	failCount int
	calls     int
}

func (f *flakyChannel) Name() string                    { return "flaky" }
func (f *flakyChannel) Start(ctx context.Context) error { return nil }
func (f *flakyChannel) Stop(ctx context.Context) error  { return nil }
func (f *flakyChannel) IsRunning() bool                 { return true }
func (f *flakyChannel) IsAllowed(senderID string) bool  { return true }
func (f *flakyChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	f.calls++
	if f.calls <= f.failCount {
		return errors.New("temporary send error")
	}
	return nil
}

func TestManagerSendWithRetryEventuallySucceeds(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.Defaults.Workspace = t.TempDir()
	m := &Manager{
		channels:   map[string]Channel{},
		bus:        bus.NewMessageBus(),
		config:     cfg,
		retryQueue: make(chan outboundRetry, 2),
	}
	ch := &flakyChannel{failCount: 2}
	err := m.sendWithRetry(context.Background(), ch, bus.OutboundMessage{
		Channel: "telegram", ChatID: "1", Content: "hello",
	}, 3)
	if err != nil {
		t.Fatalf("expected success after retries, got err=%v", err)
	}
	if ch.calls != 3 {
		t.Fatalf("expected 3 calls, got %d", ch.calls)
	}
}

func TestManagerAppendOutboundFailureWritesFile(t *testing.T) {
	ws := t.TempDir()
	cfg := &config.Config{}
	cfg.Agents.Defaults.Workspace = ws
	m := &Manager{
		channels:   map[string]Channel{},
		bus:        bus.NewMessageBus(),
		config:     cfg,
		retryQueue: make(chan outboundRetry, 2),
	}

	m.appendOutboundFailure(outboundFailureEntry{
		TS:       1,
		Channel:  "telegram",
		ChatID:   "123",
		Content:  "x",
		Error:    "failed",
		Attempts: 3,
	})

	path := filepath.Join(ws, "channels", "outbound_failures.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected failure log file, read err: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty failure log")
	}
}
