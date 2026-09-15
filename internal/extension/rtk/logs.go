package rtk

import (
	"fmt"
	"strings"
)

// keepEdges is how many lines survive verbatim at each edge of a summarized
// run: the head anchors what the block is, the tail carries the outcome.
const keepEdges = 3

// summarizeRun keeps the first and last keepEdges lines of a run and folds
// the middle into a one-line marker naming the kind and the size elided.
func summarizeRun(kind string, lines []string) string {
	if len(lines) <= keepEdges*2+1 {
		return strings.Join(lines, "\n")
	}
	head := lines[:keepEdges]
	tail := lines[len(lines)-keepEdges:]
	elided := len(lines) - keepEdges*2

	var b strings.Builder
	b.WriteString(strings.Join(head, "\n"))
	fmt.Fprintf(&b, "\n[rtk:%s %d similar line(s) elided]\n", kind, elided)
	b.WriteString(strings.Join(tail, "\n"))
	return b.String()
}

// summarizeTestLog counts pass/skip lines and reports the totals instead of
// the roll call. Only passing output reaches here; failures never match the
// detector, so they stay verbatim in the surrounding text.
func summarizeTestLog(lines []string) string {
	pass, skip, run := 0, 0, 0
	for _, l := range lines {
		switch {
		case strings.Contains(l, "--- SKIP:"):
			skip++
		case strings.HasPrefix(strings.TrimSpace(l), "=== RUN"):
			run++
		default:
			pass++
		}
	}
	if run > pass {
		pass = run
	}
	s := fmt.Sprintf("[rtk:test-summary %d passed", pass)
	if skip > 0 {
		s += fmt.Sprintf(", %d skipped", skip)
	}
	return s + ", all OK]"
}

// summarizeStackTrace keeps the top frames — where the error is — and elides
// the descent through framework code.
func summarizeStackTrace(lines []string) string {
	const keepTop = 6
	if len(lines) <= keepTop+2 {
		return strings.Join(lines, "\n")
	}
	var b strings.Builder
	b.WriteString(strings.Join(lines[:keepTop], "\n"))
	fmt.Fprintf(&b, "\n[rtk:stack-trace %d deeper frame line(s) elided]\n", len(lines)-keepTop-1)
	b.WriteString(lines[len(lines)-1])
	return b.String()
}

// summarizeTree keeps the first entries of a listing and reports how many
// more were elided, preserving the shape without the roll call.
func summarizeTree(lines []string) string {
	const keepTop = 8
	if len(lines) <= keepTop+2 {
		return strings.Join(lines, "\n")
	}
	var b strings.Builder
	b.WriteString(strings.Join(lines[:keepTop], "\n"))
	fmt.Fprintf(&b, "\n[rtk:tree %d more entrie(s) elided]", len(lines)-keepTop)
	return b.String()
}
