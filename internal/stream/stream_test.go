package stream

import (
	"os"
	"strings"
	"testing"

	"rtfm.run/internal/report"
)

// gemini_missing.jsonl is a real agy 1.5.8 stream: the agent followed a
// README that runs `echo hello` and then `gemini --version` (not installed).
func TestParseRealStream(t *testing.T) {
	f, err := os.Open("testdata/gemini_missing.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	s, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	if s.Malformed != 0 {
		t.Errorf("malformed = %d, want 0", s.Malformed)
	}
	if s.ConversationID == "" || s.Model != "gemini-3.8-flash-low" {
		t.Errorf("init: conversation=%q model=%q", s.ConversationID, s.Model)
	}
	if len(s.Commands) != 2 {
		t.Fatalf("commands = %+v, want 2", s.Commands)
	}
	if c := s.Commands[0]; c.Line != "echo hello" || c.ExitCode != 0 {
		t.Errorf("cmd0 = %+v", c)
	}
	if c := s.Commands[1]; c.Line != "gemini --version" || c.ExitCode != 127 || !strings.Contains(c.Output, "command not found") {
		t.Errorf("cmd1 = %+v", c)
	}
	if s.Result == nil || s.Result.Status != "SUCCESS" || s.Result.Usage.TotalTokens == 0 {
		t.Fatalf("result = %+v", s.Result)
	}
	if s.Report == nil {
		t.Fatal("no structured report")
	}
	if s.Report.Succeeded || len(s.Report.Steps) != 2 {
		t.Errorf("report = %+v", s.Report)
	}
	if st := s.Report.Steps[1]; st.Status != report.StatusFail || st.ExitCode != 127 {
		t.Errorf("step2 = %+v", st)
	}
}

func TestParseToleratesGarbage(t *testing.T) {
	in := "not json\n\n{\"event\":\"init\",\"conversation_id\":\"c1\",\"init\":{\"model\":\"m\"}}\n{\"event\":\"result\",\"result\":{\"status\":\"SUCCESS\",\"structured_output\":null}}\n"
	s, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if s.Malformed != 1 || s.ConversationID != "c1" || s.Report != nil || s.Result == nil {
		t.Errorf("got %+v", s)
	}
}

func TestParseCommandOutput(t *testing.T) {
	cases := []struct {
		in       string
		code     int
		contains string
	}{
		{"Created At: x\nCommand exited with code 0.\n\nCommand output:\nhello\n", 0, "hello"},
		{"Command exited with code 127.\n\nCommand output:\nbash: gemini: command not found\n", 127, "not found"},
		{"Command is still running in the background.", -1, ""},
	}
	for _, c := range cases {
		code, out := ParseCommandOutput(c.in)
		if code != c.code || !strings.Contains(out, c.contains) {
			t.Errorf("ParseCommandOutput(%q) = %d, %q", c.in, code, out)
		}
	}
}
