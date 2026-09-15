// Package rtk implements the Reduce Token Kernel: semantic compression of
// verbose machine output embedded in chat messages. It detects terminal logs,
// git diffs, tree/ls listings, stack traces and repetitive PASS/OK test
// output, and replaces them with structured summaries that preserve meaning.
//
// RTK never touches system prompts, tool schemas, function calls or JSON
// payloads; those rules live in the compressor, not the detectors.
package rtk

import "regexp"

// Detectors return the length of the run starting at lines[i], or 0 when the
// line does not open a run of their kind.

var (
	// treeChars marks pretty-printed directory trees (tree, eza, broot).
	treeLine = regexp.MustCompile(`(├──|└──|│\s{2}|\|--|` + "`--" + `)`)
	// lsLongLine matches `ls -l` style permission columns.
	lsLongLine = regexp.MustCompile(`^[bcdlps-][rwxsStT-]{9}[+@.]?\s+\d+\s`)

	// Passing test output across common runners (go test, jest, pytest, tap).
	testPassLine = regexp.MustCompile(`^\s*(=== RUN\s|--- PASS:|--- SKIP:|PASS$|PASS\s|ok\s+\S+|OK\s*\(|✓\s|✔\s|\S+ \.\.\. ok$|.* PASSED\b)`)

	// Stack trace frames: JS/Java "at", Python "File", Go goroutine headers
	// and tab-indented file:line rows.
	traceFrameLine = regexp.MustCompile(`^\s+at\s+\S+|^\s+File "[^"]+", line \d+|^goroutine \d+ \[|^\tat |^\t\S+[/\\]\S+\.\w+:\d+`)
	goFileLine     = regexp.MustCompile(`^\t\S+\.go:\d+`)

	// Timestamped or level-prefixed terminal log lines. Lines carrying errors
	// are deliberately excluded so they break the run and survive verbatim.
	logStampLine = regexp.MustCompile(`^\[?\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}|^\[?\d{2}:\d{2}:\d{2}[.,:\]\s]|^npm (WARN|notice|info)\b|^\s*(INFO|DEBUG|TRACE|WARN)[:\s\]]`)
	errorishLine = regexp.MustCompile(`(?i)\b(error|fatal|panic|exception|traceback|fail(ed|ure)?)\b`)
)

// detectTree reports how many consecutive lines starting at i belong to a
// directory tree or an `ls -l` listing.
func detectTree(lines []string, i int) int {
	n := 0
	for i+n < len(lines) {
		l := lines[i+n]
		if treeLine.MatchString(l) || lsLongLine.MatchString(l) {
			n++
			continue
		}
		break
	}
	return n
}

// detectTestLog reports the run of passing/registration test lines at i.
// FAIL lines never match, so failures always survive verbatim.
func detectTestLog(lines []string, i int) int {
	n := 0
	for i+n < len(lines) && testPassLine.MatchString(lines[i+n]) {
		n++
	}
	return n
}

// detectStackTrace reports the run of stack frames at i. Go traces alternate
// a function-name line with a tab-indented file:line, so a non-frame line is
// admitted when the next line is a Go file row.
func detectStackTrace(lines []string, i int) int {
	n := 0
	for i+n < len(lines) {
		l := lines[i+n]
		switch {
		case traceFrameLine.MatchString(l):
			n++
		case n > 0 && l != "" && i+n+1 < len(lines) && goFileLine.MatchString(lines[i+n+1]):
			// Go frame: "pkg.Func(...)" followed by "\tfile.go:123".
			n += 2
		default:
			return n
		}
	}
	return n
}

// detectTerminalLog reports the run of timestamped/level-prefixed log lines
// at i. A line mentioning an error breaks the run so it is kept verbatim.
func detectTerminalLog(lines []string, i int) int {
	n := 0
	for i+n < len(lines) {
		l := lines[i+n]
		if !logStampLine.MatchString(l) || errorishLine.MatchString(l) {
			break
		}
		n++
	}
	return n
}
