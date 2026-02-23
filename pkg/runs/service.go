package runs

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Entry struct {
	RunID        string `json:"run_id"`
	JobID        string `json:"job_id,omitempty"`
	Status       string `json:"status"`
	TS           int64  `json:"ts"`
	DurationMS   int64  `json:"duration_ms,omitempty"`
	Model        string `json:"selected_model,omitempty"`
	Attempts     string `json:"fallback_attempts,omitempty"`
	Thinking     string `json:"thinking_level,omitempty"`
	Verbose      string `json:"verbose_level,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	Error        string `json:"error,omitempty"` // Backward compatibility for existing queries.
	Source       string `json:"source,omitempty"`
	Summary      string `json:"summary,omitempty"`
}

type Service struct {
	workspace string
	logPath   string
}

func NewService(workspace string) *Service {
	return &Service{
		workspace: workspace,
		logPath:   filepath.Join(workspace, "runs", "events.jsonl"),
	}
}

func (s *Service) Append(entry Entry) error {
	if strings.TrimSpace(entry.RunID) == "" {
		return fmt.Errorf("run_id is required")
	}
	if strings.TrimSpace(entry.Status) == "" {
		return fmt.Errorf("status is required")
	}
	if entry.TS == 0 {
		entry.TS = time.Now().UnixMilli()
	}
	if entry.ErrorMessage == "" {
		entry.ErrorMessage = entry.Error
	}
	if entry.Error == "" {
		entry.Error = entry.ErrorMessage
	}

	if err := os.MkdirAll(filepath.Dir(s.logPath), 0o700); err != nil {
		return err
	}

	f, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

func (s *Service) Get(runID string) (*Entry, bool, error) {
	entries, err := s.listAll()
	if err != nil {
		return nil, false, err
	}
	for _, e := range entries {
		if e.RunID == runID {
			entry := e
			return &entry, true, nil
		}
	}
	return nil, false, nil
}

func (s *Service) List(limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 20
	}

	all, err := s.listAll()
	if err != nil {
		return nil, err
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func (s *Service) Errors(limit int) ([]Entry, error) {
	entries, err := s.listAll()
	if err != nil {
		return nil, err
	}

	filtered := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if strings.EqualFold(e.Status, "failed") || strings.EqualFold(e.Status, "error") ||
			e.ErrorCode != "" || e.ErrorMessage != "" || e.Error != "" {
			filtered = append(filtered, e)
		}
	}

	if limit <= 0 {
		limit = 20
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func (s *Service) listAll() ([]Entry, error) {
	custom, err := readEntriesFromJSONL(s.logPath)
	if err != nil {
		return nil, err
	}

	cronEntries, err := s.readCronEntries()
	if err != nil {
		return nil, err
	}

	all := append(custom, cronEntries...)
	sort.Slice(all, func(i, j int) bool {
		return all[i].TS > all[j].TS
	})
	return all, nil
}

type cronRunEntry struct {
	TS         int64  `json:"ts"`
	JobID      string `json:"jobId"`
	RunID      string `json:"runId"`
	Status     string `json:"status"`
	Error      string `json:"error"`
	DurationMS int64  `json:"durationMs"`
	Summary    string `json:"summary"`
}

func (s *Service) readCronEntries() ([]Entry, error) {
	cronDir := filepath.Join(s.workspace, "cron", "runs")
	files, err := filepath.Glob(filepath.Join(cronDir, "*.jsonl"))
	if err != nil {
		return nil, err
	}

	var result []Entry
	for _, p := range files {
		entries, err := readCronEntriesFromJSONL(p)
		if err != nil {
			return nil, err
		}
		result = append(result, entries...)
	}
	return result, nil
}

func readEntriesFromJSONL(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Entry{}, nil
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	entries := make([]Entry, 0)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if e.RunID == "" || e.Status == "" {
			continue
		}
		if e.ErrorMessage == "" {
			e.ErrorMessage = e.Error
		}
		if e.Error == "" {
			e.Error = e.ErrorMessage
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func readCronEntriesFromJSONL(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Entry{}, nil
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	entries := make([]Entry, 0)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var c cronRunEntry
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			continue
		}
		if c.RunID == "" || c.Status == "" {
			continue
		}
		entries = append(entries, Entry{
			RunID:        c.RunID,
			JobID:        c.JobID,
			Status:       normalizeCronStatus(c.Status),
			TS:           c.TS,
			DurationMS:   c.DurationMS,
			ErrorMessage: c.Error,
			Error:        c.Error,
			Source:       "cron",
			Summary:      c.Summary,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func normalizeCronStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "ok":
		return "completed"
	case "error":
		return "failed"
	default:
		return status
	}
}
