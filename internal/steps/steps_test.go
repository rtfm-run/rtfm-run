package steps

import (
	"testing"

	"rtfm.run/internal/report"
)

func TestChecksOutAndKeys(t *testing.T) {
	run := &report.Run{DocPath: "README.md",
		Steps: []report.Step{
			{Index: 1, Status: report.StatusPass, DocCommand: "$ pip install x", Command: "pip install x"},
			{Index: 2, Status: report.StatusUnsupported, DocCommand: "hello --user a", Command: "hello --name a"},
			{Index: 3, Status: report.StatusPass, DocCommand: "make test", Command: "make test"},
			{Index: 4, Status: report.StatusPass, Instruction: "Open http://localhost:8000"},
			{Index: 5, Status: report.StatusPass, DocCommand: "hello --x", Command: "hello --y"},
		},
		Commands: []report.Command{
			{Line: "pip install x", ExitCode: 0},
			{Line: "hello --name a", ExitCode: 0},
			{Line: "make test", ExitCode: 2},
		},
	}
	got := FromRun(run, "newhire")
	want := []bool{true, false, false, true, false}
	for i, s := range got {
		if s.ChecksOut != want[i] {
			t.Errorf("step %d checks_out = %v, want %v (%+v)", s.Idx, s.ChecksOut, want[i], s)
		}
	}
	if got[2].ObservedExit == nil || *got[2].ObservedExit != 2 {
		t.Errorf("observed exit for step 3 = %v", got[2].ObservedExit)
	}
	// Keys are stable across runs and ignore prompts/whitespace and line numbers.
	k1 := Key("README.md", report.Step{DocCommand: "$ pip install x", DocRef: "README.md#L10"}, 1)
	k2 := Key("README.md", report.Step{DocCommand: "pip   install x", DocRef: "README.md#L12"}, 1)
	if k1 != k2 || k1 == Key("README.md", report.Step{DocCommand: "pip install x"}, 2) || k1 == Key("other.md", report.Step{DocCommand: "pip install x"}, 1) {
		t.Error("step keys not stable or not distinct")
	}
}
