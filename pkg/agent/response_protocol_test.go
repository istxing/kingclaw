package agent

import (
	"strings"
	"testing"
)

func TestApplyExecutionResponseProtocol_NonExecutionQuestionUnchanged(t *testing.T) {
	al := &AgentLoop{}
	in := "这是普通回复。"
	got := al.applyExecutionResponseProtocol("s1", "介绍一下这个项目", in)
	if got != in {
		t.Fatalf("expected unchanged, got %q", got)
	}
}

func TestApplyExecutionResponseProtocol_WithEvidence(t *testing.T) {
	al := &AgentLoop{}
	content := "run_id=exec-1\nstatus=completed\nduration=3ms"
	got := al.applyExecutionResponseProtocol("s1", "执行了吗", content)
	if !containsAll(got, []string{"结论：已确认完成。", "证据：", "下一步："}) {
		t.Fatalf("unexpected formatted output: %q", got)
	}
	if !containsAll(got, []string{"run_id=exec-1", "status=completed"}) {
		t.Fatalf("expected evidence fields in output: %q", got)
	}
}

func TestApplyExecutionResponseProtocol_NoEvidenceUsesFixedPhrase(t *testing.T) {
	al := &AgentLoop{}
	got := al.applyExecutionResponseProtocol("s1", "执行了吗", "我不太确定")
	if !containsAll(got, []string{
		"结论：未确认完成，正在等待回执。",
		"证据：未找到可验证回执",
	}) {
		t.Fatalf("expected fixed uncertainty phrase, got %q", got)
	}
}

func TestApplyExecutionResponseProtocol_AlreadyFormattedUnchanged(t *testing.T) {
	al := &AgentLoop{}
	in := "结论：已确认完成。\n证据：run_id=exec-1\n下一步：用 /status 查询。"
	got := al.applyExecutionResponseProtocol("s1", "执行了吗", in)
	if got != in {
		t.Fatalf("expected unchanged formatted output, got %q", got)
	}
}

func TestApplyExecutionResponseProtocol_UsesRecentActionWhenNoEvidence(t *testing.T) {
	al := &AgentLoop{}
	al.recentActions.Store("s1", []actionReceipt{{
		RunID:     "exec-2",
		Tool:      "exec",
		Status:    "failed",
		ErrorCode: "command_failed",
		Error:     "exit 1",
	}})
	got := al.applyExecutionResponseProtocol("s1", "执行了吗", "还在处理中")
	if !containsAll(got, []string{"结论：已确认失败。", "recent_run_id=exec-2"}) {
		t.Fatalf("expected recent action evidence, got %q", got)
	}
}

func TestEnforceInferenceLabel(t *testing.T) {
	got := enforceInferenceLabel("这可能是网络问题。")
	if !strings.Contains(got, "注：以上包含推断，待确认。") {
		t.Fatalf("expected inference label, got %q", got)
	}
}

func containsAll(s string, subs []string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(sub) == 0 || strings.Contains(s, sub)
}
