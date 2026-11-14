package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"rtfm.run/internal/devbox"
)

// activityPoll is how often a running stage's exec log is checked for new
// records.
var activityPoll = time.Second

const maxEventCommand = 300

// watchActivity relays a running stage's activity as events while the agent
// works, so progress UIs aren't silent for minutes: each command the agent
// runs ("command") and each update it posts with report_progress
// ("progress"). It tails the devbox MCP server's exec log; the returned func
// stops it after relaying anything still unread.
//
// Command output is never relayed: events are for watching, and the full
// (redacted) record stays in the run's artifacts.
func (c Config) watchActivity(ctx context.Context, stage, path string) func() {
	if c.OnEvent == nil || path == "" {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var off int64
		var partial []byte
		drain := func() {
			f, err := os.Open(path)
			if err != nil {
				return // not created yet
			}
			defer f.Close()
			if _, err := f.Seek(off, io.SeekStart); err != nil {
				return
			}
			b, err := io.ReadAll(io.LimitReader(f, 4<<20))
			if err != nil || len(b) == 0 {
				return
			}
			off += int64(len(b))
			b = append(partial, b...)
			lines := bytes.Split(b, []byte("\n"))
			// The last element is an incomplete line (or empty after a
			// trailing newline): keep it for the next read.
			partial = append([]byte(nil), lines[len(lines)-1]...)
			for _, line := range lines[:len(lines)-1] {
				var rec devbox.ExecRecord
				if json.Unmarshal(line, &rec) != nil {
					continue
				}
				c.relay(stage, rec)
			}
		}
		t := time.NewTicker(activityPoll)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				drain()
				return
			case <-t.C:
				drain()
			}
		}
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

func (c Config) relay(stage string, rec devbox.ExecRecord) {
	switch rec.Kind {
	case "progress":
		msg := strings.TrimSpace(rec.Command)
		if msg != "" {
			c.emit("progress", map[string]any{"stage": stage, "message": clip(msg, 500)})
		}
	case "", "exec":
		ev := map[string]any{"stage": stage, "command": clip(strings.TrimSpace(rec.Command), maxEventCommand),
			"exit_code": rec.ExitCode, "duration_ms": rec.DurationMS}
		if rec.TimedOut {
			ev["timed_out"] = true
		}
		c.emit("command", ev)
	case "write_file", "edit_file":
		c.emit("command", map[string]any{"stage": stage, "file_op": rec.Kind, "path": rec.Command, "exit_code": rec.ExitCode})
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}
