# Local Patches

This file tracks patches intentionally maintained in this fork.

## Active Patches

1. `5c5286f` - `fix(agent): keep multi-tool outputs in history sanitization`
- Why: prevents Codex 400 errors caused by dropped tool outputs when an assistant turn has multiple tool calls.
- Scope: `pkg/agent/context.go`, `pkg/agent/context_sanitize_test.go`.

2. `5de8445` - `fix(agent): suppress only duplicate final replies after message tool send`
- Why: prevents final completion replies from being swallowed after a progress message is sent with `message` tool.
- Scope: `pkg/agent/loop.go`, `pkg/agent/loop_test.go`, `pkg/tools/message.go`, `pkg/tools/message_test.go`.

## Policy

- Keep this list minimal.
- For each new patch, add:
  - commit hash
  - one-line reason
  - touched files
- Remove entries when merged upstream and dropped locally.
