// KingClaw - Ultra-lightweight personal AI agent
// License: MIT

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/istxing/kingclaw/pkg/auth"
	"github.com/istxing/kingclaw/pkg/runs"
)

func statusCmd() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		return
	}
	if len(os.Args) >= 3 {
		switch os.Args[2] {
		case "runs":
			statusRunsCmd(cfg.WorkspacePath())
			return
		case "errors":
			statusErrorsCmd(cfg.WorkspacePath())
			return
		}
	}

	configPath := getConfigPath()

	fmt.Printf("%s kingclaw Status\n", logo)
	fmt.Printf("Version: %s\n", formatVersion())
	build, _ := formatBuildInfo()
	if build != "" {
		fmt.Printf("Build: %s\n", build)
	}
	fmt.Println()

	if _, err := os.Stat(configPath); err == nil {
		fmt.Println("Config:", configPath, "✓")
	} else {
		fmt.Println("Config:", configPath, "✗")
	}

	workspace := cfg.WorkspacePath()
	if _, err := os.Stat(workspace); err == nil {
		fmt.Println("Workspace:", workspace, "✓")
	} else {
		fmt.Println("Workspace:", workspace, "✗")
	}

	if _, err := os.Stat(configPath); err == nil {
		fmt.Printf("Model: %s\n", cfg.Agents.Defaults.Model)
		fmt.Printf("Strategy profile: %s\n", safeStatusValue(cfg.Strategy.Profile))
		fmt.Printf(
			"Strategy knobs: fallback(rate=%d timeout=%d auth=%d billing=%d) summary(trigger=%d retain=%d) tool(retry=%d delay_ms=%d)\n",
			cfg.Agents.Defaults.FallbackRetryRateLimit,
			cfg.Agents.Defaults.FallbackRetryTimeout,
			cfg.Agents.Defaults.FallbackRetryAuth,
			cfg.Agents.Defaults.FallbackRetryBilling,
			cfg.Agents.Defaults.SummaryTriggerPercent,
			cfg.Agents.Defaults.SummaryRetainMessages,
			cfg.Agents.Defaults.ToolRetryBudget,
			cfg.Agents.Defaults.ToolRetryDelayMS,
		)

		hasOpenRouter := cfg.Providers.OpenRouter.APIKey != ""
		hasAnthropic := cfg.Providers.Anthropic.APIKey != ""
		hasOpenAI := cfg.Providers.OpenAI.APIKey != ""
		hasGemini := cfg.Providers.Gemini.APIKey != ""
		hasZhipu := cfg.Providers.Zhipu.APIKey != ""
		hasQwen := cfg.Providers.Qwen.APIKey != ""
		hasGroq := cfg.Providers.Groq.APIKey != ""
		hasVLLM := cfg.Providers.VLLM.APIBase != ""
		hasMoonshot := cfg.Providers.Moonshot.APIKey != ""
		hasDeepSeek := cfg.Providers.DeepSeek.APIKey != ""
		hasVolcEngine := cfg.Providers.VolcEngine.APIKey != ""
		hasNvidia := cfg.Providers.Nvidia.APIKey != ""
		hasOllama := cfg.Providers.Ollama.APIBase != ""

		status := func(enabled bool) string {
			if enabled {
				return "✓"
			}
			return "not set"
		}
		fmt.Println("OpenRouter API:", status(hasOpenRouter))
		fmt.Println("Anthropic API:", status(hasAnthropic))
		fmt.Println("OpenAI API:", status(hasOpenAI))
		fmt.Println("Gemini API:", status(hasGemini))
		fmt.Println("Zhipu API:", status(hasZhipu))
		fmt.Println("Qwen API:", status(hasQwen))
		fmt.Println("Groq API:", status(hasGroq))
		fmt.Println("Moonshot API:", status(hasMoonshot))
		fmt.Println("DeepSeek API:", status(hasDeepSeek))
		fmt.Println("VolcEngine API:", status(hasVolcEngine))
		fmt.Println("Nvidia API:", status(hasNvidia))
		if hasVLLM {
			fmt.Printf("vLLM/Local: ✓ %s\n", cfg.Providers.VLLM.APIBase)
		} else {
			fmt.Println("vLLM/Local: not set")
		}
		if hasOllama {
			fmt.Printf("Ollama: ✓ %s\n", cfg.Providers.Ollama.APIBase)
		} else {
			fmt.Println("Ollama: not set")
		}

		store, _ := auth.LoadStore()
		if store != nil && len(store.Credentials) > 0 {
			fmt.Println("\nOAuth/Token Auth:")
			for provider, cred := range store.Credentials {
				status := "authenticated"
				if cred.IsExpired() {
					status = "expired"
				} else if cred.NeedsRefresh() {
					status = "needs refresh"
				}
				fmt.Printf("  %s (%s): %s\n", provider, cred.AuthMethod, status)
			}
		}
	}
	fmt.Println("\nTip: `kingclaw status runs` for recent activity, `kingclaw status errors` for failed tasks.")
}

func statusRunsCmd(workspace string) {
	svc := runs.NewService(workspace)
	entries, err := svc.List(20)
	if err != nil {
		fmt.Printf("Error reading runs: %v\n", err)
		return
	}
	if len(entries) == 0 {
		fmt.Println("No runs found.")
		return
	}
	fmt.Println("Aggregated runs (latest 20):")
	for _, e := range entries {
		ts := time.UnixMilli(e.TS).Format("2006-01-02 15:04:05")
		errMsg := e.ErrorMessage
		if errMsg == "" {
			errMsg = e.Error
		}
		if errMsg == "" {
			errMsg = "-"
		}
		fmt.Printf("[%s] run_id=%s status=%s source=%s model=%s thinking=%s verbose=%s duration=%dms error=%s\n",
			ts,
			e.RunID,
			e.Status,
			safeStatusValue(e.Source),
			safeStatusValue(e.Model),
			safeStatusValue(e.Thinking),
			safeStatusValue(e.Verbose),
			e.DurationMS,
			errMsg,
		)
	}
}

type channelFailureEntry struct {
	TS       int64  `json:"ts"`
	Channel  string `json:"channel"`
	ChatID   string `json:"chat_id"`
	Content  string `json:"content"`
	Error    string `json:"error"`
	Attempts int    `json:"attempts"`
}

func statusErrorsCmd(workspace string) {
	svc := runs.NewService(workspace)
	runErrs, err := svc.Errors(20)
	if err != nil {
		fmt.Printf("Error reading run errors: %v\n", err)
		return
	}
	channelErrs := readChannelFailures(filepath.Join(workspace, "channels", "outbound_failures.jsonl"), 20)

	type row struct {
		TS      int64
		Source  string
		Summary string
	}
	var rows []row
	for _, e := range runErrs {
		msg := e.ErrorMessage
		if msg == "" {
			msg = e.Error
		}
		rows = append(rows, row{
			TS:      e.TS,
			Source:  "runs",
			Summary: fmt.Sprintf("run_id=%s status=%s source=%s error_code=%s error=%s", e.RunID, e.Status, safeStatusValue(e.Source), safeStatusValue(e.ErrorCode), safeStatusValue(msg)),
		})
	}
	for _, e := range channelErrs {
		rows = append(rows, row{
			TS:      e.TS,
			Source:  "channels",
			Summary: fmt.Sprintf("channel=%s chat_id=%s attempts=%d error=%s", e.Channel, e.ChatID, e.Attempts, e.Error),
		})
	}
	if len(rows) == 0 {
		fmt.Println("No failed tasks found.")
		return
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].TS > rows[j].TS })
	if len(rows) > 20 {
		rows = rows[:20]
	}
	fmt.Println("Recent failed tasks (latest 20):")
	for _, r := range rows {
		ts := time.UnixMilli(r.TS).Format("2006-01-02 15:04:05")
		fmt.Printf("[%s] [%s] %s\n", ts, r.Source, r.Summary)
	}
}

func readChannelFailures(path string, limit int) []channelFailureEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	var all []channelFailureEntry
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e channelFailureEntry
		if json.Unmarshal([]byte(line), &e) == nil {
			all = append(all, e)
		}
	}
	if len(all) <= limit {
		return all
	}
	return all[len(all)-limit:]
}

func safeStatusValue(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}
