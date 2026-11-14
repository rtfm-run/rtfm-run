package fix

import (
	"context"
	"strings"
	"testing"

	"vanguard.run/internal/report"
)

const doc = "3. Say hello:\n\n   ```sh\n   .venv/bin/hello --user newhire\n   ```\n\n4. Explain:\n\n   ```sh\n   gemini -p \"Explain\"\n   ```\n"

func demoRun() *report.Run {
	return &report.Run{
		Steps: []report.Step{
			{Index: 3, Status: report.StatusUnsupported, Instruction: "Say hello", Failure: report.KindStaleCommand,
				DocCommand: ".venv/bin/hello --user newhire", Command: ".venv/bin/hello --name newhire"},
			{Index: 4, Status: report.StatusFail, Instruction: "Explain", Failure: report.KindMissingPrereq,
				DocCommand: `gemini -p "Explain"`, Command: `gemini -p "Explain"`, Diagnosis: "`gemini` isn't available"},
			{Index: 5, Status: report.StatusUnsupported, DocCommand: "make run", Command: "make start"}, // never succeeded
		},
		Commands: []report.Command{
			{Line: ".venv/bin/hello --user newhire", ExitCode: 2, Output: "hello: error: unrecognized arguments: --user newhire"},
			{Line: ".venv/bin/hello --name newhire", ExitCode: 0, Output: "Hello, newhire!"},
			{Line: `gemini -p "Explain"`, ExitCode: 127, Output: "bash: line 1: gemini: command not found"},
			{Line: "make start", ExitCode: 2},
		},
	}
}

func TestFromRun(t *testing.T) {
	out, applied := FromRun(demoRun(), doc)
	if len(applied) != 1 || applied[0].StepIndex != 3 {
		t.Fatalf("applied = %+v", applied)
	}
	if !strings.Contains(out, ".venv/bin/hello --name newhire") || strings.Contains(out, "--user") {
		t.Errorf("doc not fixed:\n%s", out)
	}
	if !strings.Contains(out, `gemini -p "Explain"`) {
		t.Errorf("unrelated step changed:\n%s", out)
	}
}

// Regression: a real run on encode/httpx replaced `$ pip install httpx` as a
// substring and corrupted `$ pip install httpx[http2]`.
func TestFromRunWholeCommandsOnly(t *testing.T) {
	doc := "```shell\n$ pip install httpx\n```\n\nHTTP/2:\n\n```shell\n$ pip install httpx[http2]\n```\n"
	run := &report.Run{
		Steps:    []report.Step{{Index: 1, Status: report.StatusUnsupported, DocCommand: "$ pip install httpx", Command: "pip install --user httpx"}},
		Commands: []report.Command{{Line: "pip install --user httpx", ExitCode: 0}},
	}
	out, applied := FromRun(run, doc)
	if len(applied) != 1 {
		t.Fatalf("applied = %+v", applied)
	}
	if !strings.Contains(out, "$ pip install --user httpx\n") {
		t.Errorf("prompt not preserved or command not replaced:\n%s", out)
	}
	if !strings.Contains(out, "$ pip install httpx[http2]") {
		t.Errorf("longer command was corrupted:\n%s", out)
	}
}

func TestFromRunRejectsMachinePaths(t *testing.T) {
	run := &report.Run{
		Steps:    []report.Step{{Index: 1, Status: report.StatusUnsupported, DocCommand: "./run.sh", Command: "/tmp/vanguard/ws-123/project/run.sh"}},
		Commands: []report.Command{{Line: "/tmp/vanguard/ws-123/project/run.sh", ExitCode: 0}},
	}
	if _, applied := FromRun(run, "run ./run.sh"); len(applied) != 0 {
		t.Errorf("applied machine-specific fix: %+v", applied)
	}
}

func TestProblems(t *testing.T) {
	p := Problems(demoRun())
	for _, want := range []string{
		"Step 3 [unsupported, stale_command]",
		"Newcomer ran instead: .venv/bin/hello --name newhire",
		"unrecognized arguments: --user",
		"Step 4 [fail, missing_prereq]",
		"gemini: command not found",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("Problems missing %q:\n%s", want, p)
		}
	}
}

func TestUnifiedDiff(t *testing.T) {
	d, err := UnifiedDiff(context.Background(), "README.md", "a\nb\nc\n", "a\nB\nc\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--- a/README.md", "+++ b/README.md", "-b", "+B"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff missing %q:\n%s", want, d)
		}
	}
	if d, _ := UnifiedDiff(context.Background(), "x", "same", "same"); d != "" {
		t.Errorf("identical diff = %q", d)
	}
}
