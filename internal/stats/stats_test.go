package stats

import (
	"strings"
	"testing"
	"time"

	"rtfm.run/internal/report"
)

func run(target string, d time.Duration, succeeded bool, errMsg string, steps ...report.Step) *report.Run {
	t0 := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	return &report.Run{Target: target, Started: t0, Ended: t0.Add(d), Succeeded: succeeded, Error: errMsg, Steps: steps,
		Usage: report.Usage{TotalTokens: 100}}
}

func TestSummarize(t *testing.T) {
	runs := []*report.Run{
		run("ok", 1*time.Minute, true, ""),
		run("ok-but-gap", 2*time.Minute, true, "",
			report.Step{Status: report.StatusUnsupported, Failure: report.KindStaleCommand}),
		run("broken-a", 3*time.Minute, false, "",
			report.Step{Status: report.StatusFail, Failure: report.KindMissingPrereq}),
		run("broken-b", 5*time.Minute, false, "",
			report.Step{Status: report.StatusFail, Failure: report.KindMissingPrereq},
			report.Step{Status: report.StatusUnsupported, Failure: report.KindDocGap}),
		run("err", 10*time.Second, false, "agy: timeout"),
		nil,
	}
	s := Summarize(runs)
	if s.Targets != 5 || s.Works != 2 || s.Broken != 2 || s.Errors != 1 || s.Issues != 3 {
		t.Errorf("counts = %+v", s)
	}
	if s.FailedSteps != 2 || s.UnsupportedSteps != 2 {
		t.Errorf("steps = %d/%d", s.FailedSteps, s.UnsupportedSteps)
	}
	if s.Kinds[report.KindMissingPrereq] != 2 || s.Kinds[report.KindStaleCommand] != 1 || s.Kinds[report.KindDocGap] != 1 {
		t.Errorf("kinds = %v", s.Kinds)
	}
	if s.MedianTimeToGiveUp != 4*time.Minute {
		t.Errorf("median give up = %s", s.MedianTimeToGiveUp)
	}
	if s.MedianDuration != 2*time.Minute || s.TotalTokens != 500 {
		t.Errorf("median=%s tokens=%d", s.MedianDuration, s.TotalTokens)
	}
	md := Markdown(s, runs)
	for _, want := range []string{"5 targets", "3 with issues", "| missing_prereq | 2 |", "| broken-b | ❌"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestBlockedOnlyWhenCredentialsAreTheOnlyIssue(t *testing.T) {
	creds := []report.Credential{{Name: "API_KEY", Status: "unavailable"}}
	onlyCreds := run("a", time.Minute, false, "", report.Step{Status: report.StatusFail, Failure: report.KindEnvironment})
	onlyCreds.Credentials = creds
	alsoBroken := run("b", time.Minute, false, "",
		report.Step{Status: report.StatusFail, Failure: report.KindEnvironment},
		report.Step{Status: report.StatusUnsupported, Failure: report.KindStaleCommand})
	alsoBroken.Credentials = creds
	if !onlyCreds.Blocked() {
		t.Error("run whose only issue is a missing credential should be blocked")
	}
	if alsoBroken.Blocked() {
		t.Error("run with genuine doc issues should be broken, not blocked")
	}
	s := Summarize([]*report.Run{onlyCreds, alsoBroken})
	if s.Blocked != 1 || s.Broken != 1 {
		t.Errorf("blocked=%d broken=%d", s.Blocked, s.Broken)
	}
}
