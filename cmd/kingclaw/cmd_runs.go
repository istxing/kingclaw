package main

import (
	"fmt"
	"os"
	"time"

	"github.com/istxing/kingclaw/pkg/runs"
)

func runsCmd() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		return
	}

	svc := runs.NewService(cfg.WorkspacePath())
	if len(os.Args) < 3 {
		printRunsList(svc, 20)
		return
	}

	sub := os.Args[2]
	switch sub {
	case "status":
		if len(os.Args) < 4 {
			fmt.Println("Usage: kingclaw runs status <run_id>")
			return
		}
		printRunStatus(svc, os.Args[3])
	case "errors":
		limit := parseRunsLimit(os.Args[3:], 20)
		printRunErrors(svc, limit)
	case "help", "-h", "--help":
		runsHelp()
	default:
		args := os.Args[2:]
		limit := parseRunsLimit(args, 20)
		printRunsList(svc, limit)
	}
}

func runsHelp() {
	fmt.Println("\nRun query commands:")
	fmt.Println("  runs [--limit N]            Show recent runs")
	fmt.Println("  runs status <run_id>        Show status by run_id")
	fmt.Println("  runs errors [--limit N]     Show recent failed runs")
}

func printRunsList(svc *runs.Service, limit int) {
	entries, err := svc.List(limit)
	if err != nil {
		fmt.Printf("Error reading runs: %v\n", err)
		return
	}
	if len(entries) == 0 {
		fmt.Println("No runs found.")
		return
	}

	fmt.Printf("\nRecent runs (latest %d):\n", len(entries))
	for _, e := range entries {
		ts := time.UnixMilli(e.TS).Format("2006-01-02 15:04:05")
		if e.JobID != "" {
			fmt.Printf("[%s] run_id=%s job_id=%s status=%s duration=%dms source=%s model=%s error=%s\n",
				ts, e.RunID, e.JobID, e.Status, e.DurationMS, safeValue(e.Source), safeValue(e.Model), safeValue(displayError(e)))
			continue
		}
		fmt.Printf("[%s] run_id=%s status=%s duration=%dms source=%s model=%s error=%s\n",
			ts, e.RunID, e.Status, e.DurationMS, safeValue(e.Source), safeValue(e.Model), safeValue(displayError(e)))
	}
}

func printRunStatus(svc *runs.Service, runID string) {
	entry, found, err := svc.Get(runID)
	if err != nil {
		fmt.Printf("Error reading status: %v\n", err)
		return
	}
	if !found || entry == nil {
		fmt.Printf("Run not found: %s\n", runID)
		return
	}
	ts := time.UnixMilli(entry.TS).Format("2006-01-02 15:04:05")
	fmt.Printf("run_id=%s\n", entry.RunID)
	fmt.Printf("job_id=%s\n", safeValue(entry.JobID))
	fmt.Printf("status=%s\n", entry.Status)
	fmt.Printf("ts=%s\n", ts)
	fmt.Printf("duration=%dms\n", entry.DurationMS)
	fmt.Printf("source=%s\n", safeValue(entry.Source))
	fmt.Printf("selected_model=%s\n", safeValue(entry.Model))
	fmt.Printf("fallback_attempts=%s\n", safeValue(entry.Attempts))
	fmt.Printf("thinking_level=%s\n", safeValue(entry.Thinking))
	fmt.Printf("verbose_level=%s\n", safeValue(entry.Verbose))
	fmt.Printf("error_code=%s\n", safeValue(entry.ErrorCode))
	fmt.Printf("error=%s\n", safeValue(displayError(*entry)))
}

func printRunErrors(svc *runs.Service, limit int) {
	entries, err := svc.Errors(limit)
	if err != nil {
		fmt.Printf("Error reading errors: %v\n", err)
		return
	}
	if len(entries) == 0 {
		fmt.Println("No failed runs found.")
		return
	}

	fmt.Printf("\nRecent failed runs (latest %d):\n", len(entries))
	for _, e := range entries {
		ts := time.UnixMilli(e.TS).Format("2006-01-02 15:04:05")
		fmt.Printf("[%s] run_id=%s status=%s duration=%dms source=%s model=%s error_code=%s error=%s\n",
			ts, e.RunID, e.Status, e.DurationMS, safeValue(e.Source), safeValue(e.Model), safeValue(e.ErrorCode), safeValue(displayError(e)))
	}
}

func parseRunsLimit(args []string, def int) int {
	limit := def
	for i := 0; i < len(args); i++ {
		if args[i] == "--limit" && i+1 < len(args) {
			var parsed int
			if _, err := fmt.Sscanf(args[i+1], "%d", &parsed); err == nil && parsed > 0 {
				limit = parsed
			}
			i++
		}
	}
	return limit
}

func safeValue(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

func displayError(e runs.Entry) string {
	if e.ErrorMessage != "" {
		return e.ErrorMessage
	}
	return e.Error
}
