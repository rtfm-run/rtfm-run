// Package stats aggregates many runs into sweep-level numbers.
package stats

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"vanguard.run/internal/report"
)

// Summary is the result of a sweep.
type Summary struct {
	Targets int `json:"targets"`
	Works   int `json:"works"`
	Broken  int `json:"broken"` // the quickstart failed for the newhire
	Errors  int `json:"errors"` // the run itself failed (timeout, crash)
	// Blocked runs needed credentials that weren't available.
	Blocked int `json:"blocked"`
	// Issues counts targets with at least one failed or unsupported step,
	// including ones that eventually "worked" thanks to outside knowledge.
	Issues int `json:"issues"`

	FailedSteps      int                        `json:"failed_steps"`
	UnsupportedSteps int                        `json:"unsupported_steps"`
	Kinds            map[report.FailureKind]int `json:"kinds"`

	// MedianTimeToGiveUp is the median wall time of broken runs: how long a
	// newcomer (the agent) spent before getting stuck.
	MedianTimeToGiveUp time.Duration `json:"median_time_to_give_up_ns"`
	MedianDuration     time.Duration `json:"median_duration_ns"`
	TotalTokens        int           `json:"total_tokens"`

	// Fix outcomes, by report.Fix* status.
	Fixes map[string]int `json:"fixes,omitempty"`
}

// Summarize aggregates runs. Nil runs are skipped.
func Summarize(runs []*report.Run) Summary {
	s := Summary{Kinds: map[report.FailureKind]int{}, Fixes: map[string]int{}}
	var all, broken []time.Duration
	for _, r := range runs {
		if r == nil {
			continue
		}
		s.Targets++
		s.TotalTokens += r.Usage.TotalTokens
		if r.Fix != nil {
			s.Fixes[r.Fix.Status]++
			s.TotalTokens += r.Fix.Usage.TotalTokens
		}
		all = append(all, r.Duration())
		switch {
		case r.Error != "":
			s.Errors++
		case r.Works():
			s.Works++
		case r.Blocked():
			s.Blocked++
		default:
			s.Broken++
			broken = append(broken, r.Duration())
		}
		_, fail, unsup := r.Counts()
		s.FailedSteps += fail
		s.UnsupportedSteps += unsup
		if fail+unsup > 0 {
			s.Issues++
		}
		for _, st := range r.Steps {
			if st.Failure != report.KindNone {
				s.Kinds[st.Failure]++
			}
		}
	}
	s.MedianDuration = median(all)
	s.MedianTimeToGiveUp = median(broken)
	return s
}

func median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	ds = slices.Clone(ds)
	slices.Sort(ds)
	n := len(ds)
	if n%2 == 1 {
		return ds[n/2]
	}
	return (ds[n/2-1] + ds[n/2]) / 2
}

func pct(a, b int) string {
	if b == 0 {
		return "0%"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(a)/float64(b))
}

// Markdown renders the sweep summary and a per-target table.
func Markdown(s Summary, runs []*report.Run) string {
	var b strings.Builder
	b.WriteString("# vanguard.run sweep\n\n")
	fmt.Fprintf(&b, "**%d targets** · ✅ %d work · ❌ %d broken (%s) · 🔑 %d blocked on credentials · ⚠️ %d run errors · **%d with issues a newcomer would hit (%s)**\n\n",
		s.Targets, s.Works, s.Broken, pct(s.Broken, s.Targets), s.Blocked, s.Errors, s.Issues, pct(s.Issues, s.Targets))
	fmt.Fprintf(&b, "Steps: %d failed, %d unsupported · Median time to give up: %s · Median run: %s · Tokens: %d\n\n",
		s.FailedSteps, s.UnsupportedSteps, s.MedianTimeToGiveUp.Round(time.Second), s.MedianDuration.Round(time.Second), s.TotalTokens)

	if len(s.Fixes) > 0 {
		fmt.Fprintf(&b, "Fixes: ✅ %d verified · 🟡 %d improved · ❌ %d not fixed · ➖ %d no change · 🔎 %d unverified · ⚠️ %d errors\n\n",
			s.Fixes[report.FixVerified], s.Fixes[report.FixImproved], s.Fixes[report.FixNotFixed],
			s.Fixes[report.FixNoChange], s.Fixes[report.FixUnverified], s.Fixes[report.FixError])
	}

	if len(s.Kinds) > 0 {
		type kv struct {
			k report.FailureKind
			n int
		}
		var kinds []kv
		for k, n := range s.Kinds {
			kinds = append(kinds, kv{k, n})
		}
		sort.Slice(kinds, func(i, j int) bool {
			if kinds[i].n != kinds[j].n {
				return kinds[i].n > kinds[j].n
			}
			return kinds[i].k < kinds[j].k
		})
		b.WriteString("| Issue type | Steps |\n|---|---|\n")
		for _, e := range kinds {
			fmt.Fprintf(&b, "| %s | %d |\n", e.k, e.n)
		}
		b.WriteString("\n")
	}

	b.WriteString("| Target | Verdict | Pass | Fail | Unsupported | Time | First issue |\n|---|---|---|---|---|---|---|\n")
	for _, r := range runs {
		if r == nil {
			continue
		}
		verdict := "❌"
		switch {
		case r.Error != "":
			verdict = "⚠️"
		case r.Works():
			verdict = "✅"
		case r.Blocked():
			verdict = "🔑"
		}
		pass, fail, unsup := r.Counts()
		first := ""
		if r.Error != "" {
			first = r.Error
		} else {
			for _, st := range r.Steps {
				if st.Failure != report.KindNone {
					first = fmt.Sprintf("%s: %s", st.Failure, st.Diagnosis)
					break
				}
			}
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %s | %s |\n",
			r.Target, verdict, pass, fail, unsup, r.Duration().Round(time.Second), cell(first))
	}
	return b.String()
}

func cell(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ")
	if r := []rune(s); len(r) > 110 {
		s = string(r[:107]) + "..."
	}
	return s
}
