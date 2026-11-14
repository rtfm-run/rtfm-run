package diagnose

import (
	"testing"

	"rtfm.run/internal/report"
)

func TestSelfClone(t *testing.T) {
	collision := "fatal: destination path 'pysc2' already exists and is not an empty directory."
	cases := []struct {
		name string
		step report.Step
		want report.Status
	}{
		{"upstream URL of a fork", report.Step{Status: report.StatusFail, DocCommand: "git clone https://github.com/deepmind/pysc2.git", Output: collision}, report.StatusPass},
		{"with cd", report.Step{Status: report.StatusFail, DocCommand: "$ git clone --depth 1 git@github.com:deepmind/pysc2.git && cd pysc2", Output: collision}, report.StatusPass},
		{"agent already treated it as done", report.Step{Status: report.StatusUnsupported, DocCommand: "git clone https://github.com/deepmind/pysc2"}, report.StatusPass},
		{"different repository", report.Step{Status: report.StatusFail, DocCommand: "git clone https://github.com/Blizzard/s2client-proto.git", Output: collision}, report.StatusFail},
		{"clone plus a real step", report.Step{Status: report.StatusFail, DocCommand: "git clone https://github.com/deepmind/pysc2.git\npip install --upgrade pysc2/", Output: collision}, report.StatusFail},
		{"a real clone failure", report.Step{Status: report.StatusFail, DocCommand: "git clone https://github.com/deepmind/pysc2.git", Output: "fatal: repository not found"}, report.StatusFail},
	}
	for _, c := range cases {
		run := &report.Run{Repo: "https://github.com/kevinroleke/pysc2", Steps: []report.Step{c.step}}
		Apply(run)
		s := run.Steps[0]
		if s.Status != c.want {
			t.Errorf("%s: status = %s, want %s", c.name, s.Status, c.want)
		}
		if c.want == report.StatusPass && (s.Failure != report.KindNone || s.Note == "") {
			t.Errorf("%s: failure=%q note=%q", c.name, s.Failure, s.Note)
		}
	}
}
