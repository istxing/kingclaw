package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type regressionCase struct {
	CaseID      string   `json:"case_id"`
	UserMessage string   `json:"user_message"`
	LLMOutput   string   `json:"llm_output"`
	MustContain []string `json:"must_contain"`
}

func TestDialogueEvalRegression(t *testing.T) {
	path := filepath.Join("testdata", "dialogue_eval_cases.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dataset: %v", err)
	}
	var cases []regressionCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatalf("parse dataset: %v", err)
	}

	al := &AgentLoop{}
	for _, tc := range cases {
		got := al.applyExecutionResponseProtocol("eval-session", tc.UserMessage, tc.LLMOutput)
		for _, expect := range tc.MustContain {
			if !strings.Contains(got, expect) {
				t.Fatalf("case %s expected output contains %q, got: %q", tc.CaseID, expect, got)
			}
		}
	}
}
