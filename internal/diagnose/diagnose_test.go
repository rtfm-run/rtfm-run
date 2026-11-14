package diagnose

import (
	"strings"
	"testing"

	"vanguard.run/internal/report"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name, text string
		code       int
		kind       report.FailureKind
		contains   string
	}{
		{"bash missing tool", "bash: line 1: gemini: command not found", 127, report.KindMissingPrereq, "`gemini`"},
		{"sh missing tool", "sh: 1: yarn: not found", 127, report.KindMissingPrereq, "`yarn`"},
		{"argparse flag", "usage: hello [-h] --name NAME\nhello: error: unrecognized arguments: --user newhire", 2, report.KindStaleCommand, "`--user`"},
		{"argparse required", "hello: error: the following arguments are required: --name", 2, report.KindStaleCommand, "--name"},
		{"cobra flag", "Error: unknown flag: --verbose", 1, report.KindStaleCommand, "verbose"},
		{"go flag", "flag provided but not defined: -fast", 2, report.KindStaleCommand, "-fast"},
		{"git subcommand", "git: 'pul' is not a git command. See 'git --help'.", 1, report.KindStaleCommand, "not accepted"},
		{"python module", "ModuleNotFoundError: No module named 'requests'", 1, report.KindMissingPrereq, "`requests`"},
		{"node module", "Error: Cannot find module 'express'", 1, report.KindMissingPrereq, "`express`"},
		{"deprecated model", "404 models/old-model is not found for API version v1beta", 1, report.KindDeprecatedAPI, "deprecated"},
		{"deprecated generic", "WARNING: this command has been removed in v3", 1, report.KindDeprecatedAPI, "deprecated"},
		{"api key", "Error: GEMINI_API_KEY environment variable not found", 41, report.KindEnvironment, "credentials"},
		{"401", "HTTP 401 Unauthorized", 1, report.KindEnvironment, "credentials"},
		{"404 url", "curl: (22) The requested URL returned error: 404", 22, report.KindBrokenLink, "doesn't exist"},
		{"missing file", "cat: config.example.yaml: No such file or directory", 1, report.KindBrokenLink, "doesn't exist"},
		{"unknown", "segfault", 139, report.KindUnknown, "exit 139"},
	}
	for _, c := range cases {
		k, d := Classify(c.text, c.code)
		if k != c.kind || !strings.Contains(d, c.contains) {
			t.Errorf("%s: Classify = %s, %q; want %s containing %q", c.name, k, d, c.kind, c.contains)
		}
	}
}

func TestApply(t *testing.T) {
	run := &report.Run{
		Steps: []report.Step{
			{Index: 1, Status: report.StatusPass, Command: "python3 -m venv .venv"},
			{Index: 2, Status: report.StatusUnsupported, DocCommand: ".venv/bin/hello --user newhire", Command: ".venv/bin/hello --name newhire"},
			{Index: 3, Status: report.StatusFail, Command: `gemini -p "hi"`, ExitCode: 127},
			{Index: 4, Status: report.StatusUnsupported, Note: "Docs never say to create a .env file."},
			{Index: 5, Status: report.StatusPass, Command: "make test"},
		},
		Commands: []report.Command{
			{Line: "python3 -m venv .venv", ExitCode: 0},
			{Line: ".venv/bin/hello --user newhire", ExitCode: 2, Output: "hello: error: unrecognized arguments: --user newhire"},
			{Line: ".venv/bin/hello --name newhire", ExitCode: 0, Output: "Hello, newhire!"},
			{Line: `gemini  -p "hi"`, ExitCode: 127, Output: "bash: line 1: gemini: command not found"},
			{Line: "make test", ExitCode: 2, Output: "FAIL"},
		},
	}
	Apply(run)
	s := run.Steps
	if s[0].Failure != "" || s[0].Diagnosis != "" {
		t.Errorf("step1 = %+v", s[0])
	}
	if s[1].Failure != report.KindStaleCommand || !strings.Contains(s[1].Diagnosis, "what actually worked was `.venv/bin/hello --name newhire`") || !strings.Contains(s[1].Diagnosis, "`--user`") {
		t.Errorf("step2 = %+v", s[1])
	}
	if s[2].Failure != report.KindMissingPrereq || !strings.Contains(s[2].Diagnosis, "`gemini`") {
		t.Errorf("step3 = %+v", s[2])
	}
	if s[3].Failure != report.KindDocGap || !strings.Contains(s[3].Diagnosis, ".env") {
		t.Errorf("step4 = %+v", s[3])
	}
	if !strings.Contains(s[4].Diagnosis, "exited 2") {
		t.Errorf("step5 mismatch not flagged: %+v", s[4])
	}
}

// Real sequence from a devbox run: the tool is missing, the agent installs
// it on its own, and then it needs credentials the docs never mention.
func TestApplyMultipleAttempts(t *testing.T) {
	cmd := `gemini -p "Explain"`
	run := &report.Run{
		Steps: []report.Step{{Index: 4, Status: report.StatusFail, DocCommand: cmd, Command: cmd, ExitCode: 41}},
		Commands: []report.Command{
			{Line: cmd, ExitCode: 127, Output: "bash: line 1: gemini: command not found"},
			{Line: "npm install -g @google/gemini-cli", ExitCode: 0},
			{Line: cmd, ExitCode: 41, Output: "Please set an Auth method ... or specify one of the following environment variables before running: GEMINI_API_KEY"},
		},
	}
	Apply(run)
	s := run.Steps[0]
	if s.Failure != report.KindMissingPrereq {
		t.Errorf("primary = %s, want missing_prereq", s.Failure)
	}
	if !strings.Contains(s.Diagnosis, "`gemini`") || !strings.Contains(s.Diagnosis, "Then: The step needs credentials") {
		t.Errorf("diagnosis = %q", s.Diagnosis)
	}
}
