// Package stream parses `agy --output-format stream-json` output.
//
// The format (agy 1.5.8) is NDJSON, one object per line, keyed by "event":
//
//	{"event":"init","conversation_id":…,"init":{"model":…,"cwd":…,"tools":[…]}}
//	{"event":"step_update","step_update":{"step_index":N,"state":"ACTIVE|DONE",
//	    "step_type":"user_input|agent_response|tool","tool_name":…,
//	    "tool_info":{"name":…,"parameters":{…},"output":"…"},"text_delta":…}}
//	{"event":"result","result":{"status":"SUCCESS",…,"structured_output":{…},"usage":{…}}}
package stream

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"rtfm.run/internal/report"
)

// Event is one decoded line.
type Event struct {
	Event          string      `json:"event"`
	ConversationID string      `json:"conversation_id"`
	Init           *Init       `json:"init"`
	StepUpdate     *StepUpdate `json:"step_update"`
	Result         *Result     `json:"result"`
}

type Init struct {
	Model          string   `json:"model"`
	Cwd            string   `json:"cwd"`
	Tools          []string `json:"tools"`
	PermissionMode string   `json:"permission_mode"`
}

type StepUpdate struct {
	ConversationID string    `json:"conversation_id"`
	StepIndex      int       `json:"step_index"`
	State          string    `json:"state"`
	StepType       string    `json:"step_type"`
	ToolName       string    `json:"tool_name"`
	ToolInfo       *ToolInfo `json:"tool_info"`
	TextDelta      string    `json:"text_delta"`
}

type ToolInfo struct {
	Name       string         `json:"name"`
	Parameters map[string]any `json:"parameters"`
	Output     string         `json:"output"`
}

type Result struct {
	ConversationID   string          `json:"conversation_id"`
	Status           string          `json:"status"`
	Response         string          `json:"response"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	DurationSeconds  float64         `json:"duration_seconds"`
	NumTurns         int             `json:"num_turns"`
	Usage            report.Usage    `json:"usage"`
}

// AgentReport is the newhire's structured self-report.
type AgentReport struct {
	Succeeded    bool          `json:"succeeded"`
	GaveUpReason string        `json:"gave_up_reason"`
	Steps        []report.Step `json:"steps"`
}

// Summary is everything extracted from one stream.
type Summary struct {
	ConversationID string
	Model          string
	Commands       []report.Command
	Searches       []string
	URLsRead       []string
	Result         *Result
	Report         *AgentReport
	Malformed      int    // lines that were not valid JSON
	Stderr         string // agy's stderr (soft-denied tool notices, warnings)
}

// Parse reads a whole stream. Non-JSON lines are counted and skipped.
func Parse(r io.Reader) (*Summary, error) {
	s := &Summary{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 32<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			s.Malformed++
			continue
		}
		s.apply(&ev)
	}
	if err := sc.Err(); err != nil {
		return s, fmt.Errorf("stream: read: %w", err)
	}
	if s.Result != nil && len(s.Result.StructuredOutput) > 0 && string(s.Result.StructuredOutput) != "null" {
		var rep AgentReport
		if err := json.Unmarshal(s.Result.StructuredOutput, &rep); err != nil {
			return s, fmt.Errorf("stream: structured_output: %w", err)
		}
		s.Report = &rep
	}
	return s, nil
}

func (s *Summary) apply(ev *Event) {
	switch ev.Event {
	case "init":
		s.ConversationID = ev.ConversationID
		if ev.Init != nil {
			s.Model = ev.Init.Model
		}
	case "step_update":
		u := ev.StepUpdate
		if u == nil || u.State != "DONE" || u.StepType != "tool" || u.ToolInfo == nil {
			return
		}
		switch u.ToolName {
		case "run_command":
			line := str(u.ToolInfo.Parameters, "CommandLine")
			code, out := ParseCommandOutput(u.ToolInfo.Output)
			s.Commands = append(s.Commands, report.Command{Line: line, ExitCode: code, Output: tail(out, 2000)})
		case "search_web":
			if q := firstStr(u.ToolInfo.Parameters, "query", "Query", "q"); q != "" {
				s.Searches = append(s.Searches, q)
			}
		case "read_url_content", "open_browser_url":
			if u := firstStr(u.ToolInfo.Parameters, "Url", "url", "URL"); u != "" {
				s.URLsRead = append(s.URLsRead, u)
			}
		}
	case "result":
		s.Result = ev.Result
		if s.ConversationID == "" && ev.Result != nil {
			s.ConversationID = ev.Result.ConversationID
		}
	}
}

var exitRe = regexp.MustCompile(`Command exited with code (-?\d+)`)

// ParseCommandOutput extracts the exit code and the command's own output from
// a run_command tool result. Exit code is -1 when not present (e.g. the
// command is still running in the background).
func ParseCommandOutput(s string) (exitCode int, output string) {
	exitCode = -1
	if m := exitRe.FindStringSubmatch(s); m != nil {
		exitCode, _ = strconv.Atoi(m[1])
	}
	if _, after, ok := strings.Cut(s, "Command output:\n"); ok {
		output = strings.TrimRight(after, "\n")
	}
	return
}

func str(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

func firstStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v := str(m, k); v != "" {
			return v
		}
	}
	return ""
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
