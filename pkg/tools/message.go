package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/istxing/kingclaw/pkg/runs"
)

type SendCallback func(channel, chatID, content string) error

type MessageTool struct {
	sendCallback   SendCallback
	defaultChannel string
	defaultChatID  string
	workspace      string
	sentInRound    bool // Tracks whether a message was sent in the current processing round
	sentMessages   []sentMessage
}

type sentMessage struct {
	channel string
	chatID  string
	content string
}

func NewMessageTool(workspace ...string) *MessageTool {
	ws := ""
	if len(workspace) > 0 {
		ws = workspace[0]
	}
	return &MessageTool{workspace: ws}
}

func (t *MessageTool) Name() string {
	return "message"
}

func (t *MessageTool) Description() string {
	return "Send a message to user on a chat channel. Use this when you want to communicate something."
}

func (t *MessageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content": map[string]any{
				"type":        "string",
				"description": "The message content to send",
			},
			"channel": map[string]any{
				"type":        "string",
				"description": "Optional: target channel (telegram, whatsapp, etc.)",
			},
			"chat_id": map[string]any{
				"type":        "string",
				"description": "Optional: target chat/user ID",
			},
		},
		"required": []string{"content"},
	}
}

func (t *MessageTool) SetContext(channel, chatID string) {
	t.defaultChannel = channel
	t.defaultChatID = chatID
	t.sentInRound = false // Reset send tracking for new processing round
	t.sentMessages = nil
}

// HasSentInRound returns true if the message tool sent a message during the current round.
func (t *MessageTool) HasSentInRound() bool {
	return t.sentInRound
}

// HasSentMatchingInRound returns true when the given message exactly matches
// one message sent by this tool in the current turn for the same channel/chat.
func (t *MessageTool) HasSentMatchingInRound(channel, chatID, content string) bool {
	want := normalizeMessage(content)
	if want == "" {
		return false
	}
	for _, msg := range t.sentMessages {
		if msg.channel == channel && msg.chatID == chatID && normalizeMessage(msg.content) == want {
			return true
		}
	}
	return false
}

func (t *MessageTool) SetSendCallback(callback SendCallback) {
	t.sendCallback = callback
}

func (t *MessageTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	runID := newRunID("message")
	startAt := time.Now()
	record := func(status, errCode, errMsg, summary string) {
		appendRunLog(t.workspace, runs.Entry{
			RunID:        runID,
			Status:       status,
			TS:           startAt.UnixMilli(),
			DurationMS:   time.Since(startAt).Milliseconds(),
			ErrorCode:    errCode,
			ErrorMessage: errMsg,
			Error:        errMsg,
			Source:       "message",
			Summary:      summary,
		})
	}

	content, ok := args["content"].(string)
	if !ok {
		record("failed", "invalid_args", "content is required", "invalid args")
		return &ToolResult{
			ForLLM:  fmt.Sprintf("[message][run_id=%s] status=failed error_code=invalid_args error=content is required", runID),
			IsError: true,
		}
	}

	channel, _ := args["channel"].(string)
	chatID, _ := args["chat_id"].(string)

	if channel == "" {
		channel = t.defaultChannel
	}
	if chatID == "" {
		chatID = t.defaultChatID
	}

	if channel == "" || chatID == "" {
		record("failed", "missing_target", "No target channel/chat specified", "missing target")
		return &ToolResult{
			ForLLM:  fmt.Sprintf("[message][run_id=%s] status=failed error_code=missing_target error=No target channel/chat specified", runID),
			IsError: true,
		}
	}

	if t.sendCallback == nil {
		record("failed", "not_configured", "Message sending not configured", "not configured")
		return &ToolResult{
			ForLLM:  fmt.Sprintf("[message][run_id=%s] status=failed error_code=not_configured error=Message sending not configured", runID),
			IsError: true,
		}
	}

	if err := t.sendCallback(channel, chatID, content); err != nil {
		record("failed", "send_failed", err.Error(), "send failed")
		return &ToolResult{
			ForLLM:  fmt.Sprintf("[message][run_id=%s] status=failed error_code=send_failed error=sending message: %v", runID, err),
			IsError: true,
			Err:     err,
		}
	}

	t.sentInRound = true
	t.sentMessages = append(t.sentMessages, sentMessage{
		channel: channel,
		chatID:  chatID,
		content: content,
	})
	record("completed", "", "", fmt.Sprintf("channel=%s chat_id=%s", channel, chatID))
	// Silent: user already received the message directly
	return &ToolResult{
		ForLLM: fmt.Sprintf("[message][run_id=%s] status=completed channel=%s chat_id=%s", runID, channel, chatID),
		Silent: true,
	}
}

func normalizeMessage(content string) string {
	return strings.TrimSpace(content)
}
