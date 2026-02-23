package tools

import (
	"strings"
	"time"

	"github.com/istxing/kingclaw/pkg/runs"
)

func appendRunLog(workspace string, entry runs.Entry) {
	ws := strings.TrimSpace(workspace)
	if ws == "" {
		return
	}
	if entry.TS == 0 {
		entry.TS = time.Now().UnixMilli()
	}
	_ = runs.NewService(ws).Append(entry)
}
