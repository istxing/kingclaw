package tools

import (
	"context"
	"fmt"

	"github.com/istxing/kingclaw/pkg/runs"
)

type SpawnTool struct {
	manager        *SubagentManager
	originChannel  string
	originChatID   string
	allowlistCheck func(targetAgentID string) bool
	callback       AsyncCallback // For async completion notification
}

func NewSpawnTool(manager *SubagentManager) *SpawnTool {
	return &SpawnTool{
		manager:       manager,
		originChannel: "cli",
		originChatID:  "direct",
	}
}

// SetCallback implements AsyncTool interface for async completion notification
func (t *SpawnTool) SetCallback(cb AsyncCallback) {
	t.callback = cb
}

func (t *SpawnTool) Name() string {
	return "spawn"
}

func (t *SpawnTool) Description() string {
	return "Spawn a subagent to handle a task in the background. Use this for complex or time-consuming tasks that can run independently. The subagent will complete the task and report back when done."
}

func (t *SpawnTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{
				"type":        "string",
				"description": "The task for subagent to complete",
			},
			"label": map[string]any{
				"type":        "string",
				"description": "Optional short label for the task (for display)",
			},
			"agent_id": map[string]any{
				"type":        "string",
				"description": "Optional target agent ID to delegate the task to",
			},
		},
		"required": []string{"task"},
	}
}

func (t *SpawnTool) SetContext(channel, chatID string) {
	t.originChannel = channel
	t.originChatID = chatID
}

func (t *SpawnTool) SetAllowlistChecker(check func(targetAgentID string) bool) {
	t.allowlistCheck = check
}

func (t *SpawnTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	runID := newRunID("subagent")
	recordFailed := func(code, msg string) {
		workspace := ""
		if t.manager != nil {
			workspace = t.manager.workspace
		}
		appendRunLog(workspace, runs.Entry{
			RunID:        runID,
			Status:       "failed",
			ErrorCode:    code,
			ErrorMessage: msg,
			Error:        msg,
			Source:       "subagent",
		})
	}

	task, ok := args["task"].(string)
	if !ok {
		recordFailed("invalid_args", "task is required")
		return ErrorResult(
			fmt.Sprintf("[subagent][run_id=%s] status=failed error_code=invalid_args error=task is required", runID),
		)
	}

	label, _ := args["label"].(string)
	agentID, _ := args["agent_id"].(string)

	// Check allowlist if targeting a specific agent
	if agentID != "" && t.allowlistCheck != nil {
		if !t.allowlistCheck(agentID) {
			recordFailed("not_allowed", fmt.Sprintf("not allowed to spawn agent '%s'", agentID))
			return ErrorResult(
				fmt.Sprintf(
					"[subagent][run_id=%s] status=failed error_code=not_allowed error=not allowed to spawn agent '%s'",
					runID,
					agentID,
				),
			)
		}
	}

	if t.manager == nil {
		recordFailed("not_configured", "Subagent manager not configured")
		return ErrorResult(
			fmt.Sprintf("[subagent][run_id=%s] status=failed error_code=not_configured error=Subagent manager not configured", runID),
		)
	}

	// Pass callback to manager for async completion notification
	result, err := t.manager.Spawn(ctx, task, label, agentID, t.originChannel, t.originChatID, t.callback)
	if err != nil {
		recordFailed("spawn_failed", fmt.Sprintf("failed to spawn subagent: %v", err))
		return ErrorResult(
			fmt.Sprintf("[subagent][run_id=%s] status=failed error_code=spawn_failed error=failed to spawn subagent: %v", runID, err),
		)
	}

	// Return AsyncResult since the task runs in background
	return AsyncResult(result)
}
