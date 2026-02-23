package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type EvalCase struct {
	CaseID      string   `json:"case_id"`
	UserMessage string   `json:"user_message"`
	LLMOutput   string   `json:"llm_output"`
	MustContain []string `json:"must_contain"`
}

type EvalResult struct {
	CaseID          string `json:"case_id"`
	Passed          bool   `json:"passed"`
	ReceiptComplete bool   `json:"receipt_complete"`
	Contradiction   bool   `json:"contradiction"`
	RepairRounds    int    `json:"repair_rounds"`
	Notes           string `json:"notes,omitempty"`
}

func evalCmd() {
	if len(os.Args) < 3 {
		evalHelp()
		return
	}
	switch os.Args[2] {
	case "run":
		evalRunCmd()
	case "compare":
		evalCompareCmd()
	case "help", "-h", "--help":
		evalHelp()
	default:
		fmt.Printf("Unknown eval command: %s\n", os.Args[2])
		evalHelp()
	}
}

func evalHelp() {
	fmt.Println("\nEval commands:")
	fmt.Println("  eval run [--cases <path>] [--out <path>]       Run baseline evaluation")
	fmt.Println("  eval compare --kingclaw <file> --openclaw <file>  Compare two eval result files")
}

func evalRunCmd() {
	casesPath := "pkg/agent/testdata/dialogue_eval_cases.json"
	outPath := ""
	args := os.Args[3:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--cases":
			if i+1 < len(args) {
				casesPath = args[i+1]
				i++
			}
		case "--out":
			if i+1 < len(args) {
				outPath = args[i+1]
				i++
			}
		}
	}

	data, err := os.ReadFile(casesPath)
	if err != nil {
		fmt.Printf("Error reading cases: %v\n", err)
		return
	}
	var cases []EvalCase
	if err := json.Unmarshal(data, &cases); err != nil {
		fmt.Printf("Error parsing cases: %v\n", err)
		return
	}

	results := make([]EvalResult, 0, len(cases))
	passCount := 0
	receiptOK := 0
	contradictions := 0
	totalRepairRounds := 0
	for _, c := range cases {
		passed := true
		for _, need := range c.MustContain {
			if need != "" && !containsString(c.LLMOutput, need) {
				passed = false
				break
			}
		}
		receiptComplete := containsString(c.LLMOutput, "run_id") || containsString(c.LLMOutput, "回执")
		contradiction := containsString(c.LLMOutput, "已完成") && containsString(c.LLMOutput, "未确认")
		repairRounds := 0
		if !passed {
			repairRounds = 1
		}
		results = append(results, EvalResult{
			CaseID:          c.CaseID,
			Passed:          passed,
			ReceiptComplete: receiptComplete,
			Contradiction:   contradiction,
			RepairRounds:    repairRounds,
		})
		if passed {
			passCount++
		}
		if receiptComplete {
			receiptOK++
		}
		if contradiction {
			contradictions++
		}
		totalRepairRounds += repairRounds
	}

	fmt.Printf("Cases: %d\n", len(cases))
	fmt.Printf("Pass rate: %.1f%%\n", percent(passCount, len(cases)))
	fmt.Printf("Receipt completeness: %.1f%%\n", percent(receiptOK, len(cases)))
	fmt.Printf("Contradiction rate: %.1f%%\n", percent(contradictions, len(cases)))
	avgRepair := 0.0
	if len(cases) > 0 {
		avgRepair = float64(totalRepairRounds) / float64(len(cases))
	}
	fmt.Printf("Avg repair rounds: %.2f\n", avgRepair)

	if outPath != "" {
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			fmt.Printf("Error creating output dir: %v\n", err)
			return
		}
		b, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(outPath, b, 0o600); err != nil {
			fmt.Printf("Error writing output: %v\n", err)
			return
		}
		fmt.Printf("Saved: %s\n", outPath)
	}
}

func evalCompareCmd() {
	kingclawPath := ""
	openclawPath := ""
	args := os.Args[3:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--kingclaw":
			if i+1 < len(args) {
				kingclawPath = args[i+1]
				i++
			}
		case "--openclaw":
			if i+1 < len(args) {
				openclawPath = args[i+1]
				i++
			}
		}
	}
	if kingclawPath == "" || openclawPath == "" {
		fmt.Println("Usage: kingclaw eval compare --kingclaw <file> --openclaw <file>")
		return
	}

	kc, err := loadEvalResults(kingclawPath)
	if err != nil {
		fmt.Printf("Error reading kingclaw results: %v\n", err)
		return
	}
	oc, err := loadEvalResults(openclawPath)
	if err != nil {
		fmt.Printf("Error reading openclaw results: %v\n", err)
		return
	}

	kStats := summarizeResults(kc)
	oStats := summarizeResults(oc)
	fmt.Println("Comparison:")
	fmt.Printf("  pass_rate:         kingclaw=%.1f%% openclaw=%.1f%% delta=%+.1f%%\n", kStats.PassRate, oStats.PassRate, kStats.PassRate-oStats.PassRate)
	fmt.Printf("  receipt_complete:  kingclaw=%.1f%% openclaw=%.1f%% delta=%+.1f%%\n", kStats.ReceiptRate, oStats.ReceiptRate, kStats.ReceiptRate-oStats.ReceiptRate)
	fmt.Printf("  contradiction:     kingclaw=%.1f%% openclaw=%.1f%% delta=%+.1f%% (lower is better)\n", kStats.ContradictionRate, oStats.ContradictionRate, kStats.ContradictionRate-oStats.ContradictionRate)
	fmt.Printf("  avg_repair_rounds: kingclaw=%.2f openclaw=%.2f delta=%+.2f (lower is better)\n", kStats.AvgRepair, oStats.AvgRepair, kStats.AvgRepair-oStats.AvgRepair)
}

type evalStats struct {
	PassRate          float64
	ReceiptRate       float64
	ContradictionRate float64
	AvgRepair         float64
}

func summarizeResults(results []EvalResult) evalStats {
	n := len(results)
	if n == 0 {
		return evalStats{}
	}
	pass, receipt, contradiction, repair := 0, 0, 0, 0
	for _, r := range results {
		if r.Passed {
			pass++
		}
		if r.ReceiptComplete {
			receipt++
		}
		if r.Contradiction {
			contradiction++
		}
		repair += r.RepairRounds
	}
	return evalStats{
		PassRate:          percent(pass, n),
		ReceiptRate:       percent(receipt, n),
		ContradictionRate: percent(contradiction, n),
		AvgRepair:         float64(repair) / float64(n),
	}
}

func loadEvalResults(path string) ([]EvalResult, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []EvalResult
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CaseID < out[j].CaseID })
	return out, nil
}

func percent(v, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(v) * 100.0 / float64(total)
}

func containsString(s, sub string) bool {
	return sub == "" || (len(s) >= len(sub) && (stringIndex(s, sub) >= 0))
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
