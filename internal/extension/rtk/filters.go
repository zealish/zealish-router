package rtk

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	reGitLog    = regexp.MustCompile(`^[*|/\\ ]*commit [0-9a-f]{7,40}$`)
	reGitStatus = regexp.MustCompile(`^(On branch |nothing to commit|Changes (not |to be )|Untracked files:)`)
	reBuild     = regexp.MustCompile(`(?i)^(npm (warn|error|ERR!)|yarn (warn|error)|\s*(Compiling|Downloading)\s+\S+|added \d+ package|\[ERROR\]|BUILD (SUCCESS|FAILED)|\s*Finished\s+|Successfully (installed|built)|ERROR:)`)
	reGrep      = regexp.MustCompile(`^[^:\n]+:\d+:`)
	reTree      = regexp.MustCompile(`[├└]──|│  `)
	reLS        = regexp.MustCompile(`^[-dlbcps][rwx-]{9}`)
	reStamp     = regexp.MustCompile(`^\[?\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}|^\[?\d{2}:\d{2}:\d{2}[.,:]`)
	rePass      = regexp.MustCompile(`^\s*(=== RUN\s|--- PASS:|--- SKIP:|PASS$|PASS\s|ok\s+\S+|OK\s*\(|✓\s|✔\s|.* PASSED\b)`)
)

func filterText(text string) string {
	lines := strings.Split(text, "\n")
	kind := detectKind(lines)
	if kind == "" {
		return text
	}
	var out []string
	changed := false
	for i := 0; i < len(lines); {
		n := runLength(lines, i, kind)
		if n >= 8 {
			out = append(out, summarize(kind, lines[i:i+n]))
			i += n
			changed = true
		} else {
			out = append(out, lines[i])
			i++
		}
	}
	if !changed {
		return text
	}
	return strings.Join(out, "\n")
}
func detectKind(lines []string) string {
	limit := len(lines)
	if limit > 1024 {
		limit = 1024
	}
	for i := 0; i < limit; i++ {
		l := lines[i]
		switch {
		case reGitLog.MatchString(l):
			return "git-log"
		case strings.HasPrefix(l, "diff --git ") || strings.HasPrefix(l, "@@ "):
			return "diff"
		case reGitStatus.MatchString(l):
			return "git-status"
		case reBuild.MatchString(l):
			return "build"
		case reTree.MatchString(l):
			return "tree"
		case reLS.MatchString(l):
			return "ls"
		case rePass.MatchString(l):
			return "test"
		case reStamp.MatchString(l):
			return "log"
		}
	}
	non := []string{}
	for _, l := range lines[:limit] {
		if strings.TrimSpace(l) != "" {
			non = append(non, l)
		}
	}
	for i := 0; i < len(non) && i < 5; i++ {
		if reGrep.MatchString(non[i]) {
			return "grep"
		}
	}
	if len(non) >= 3 {
		all := true
		for _, l := range non {
			if strings.Contains(l, ":") {
				all = false
				break
			}
		}
		if all {
			return "find"
		}
	}
	return ""
}
func runLength(lines []string, i int, kind string) int {
	n := 0
	for i+n < len(lines) {
		l := lines[i+n]
		ok := false
		switch kind {
		case "diff":
			ok = strings.HasPrefix(l, "diff --git ") || strings.HasPrefix(l, "index ") || strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ ") || strings.HasPrefix(l, "@@ ") || strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-") || strings.HasPrefix(l, " ") || strings.HasPrefix(l, "new file mode") || strings.HasPrefix(l, "deleted file mode") || strings.HasPrefix(l, "rename ")
		case "tree", "ls":
			ok = reTree.MatchString(l) || reLS.MatchString(l)
		case "test":
			ok = rePass.MatchString(l)
		case "log":
			ok = reStamp.MatchString(l) && !strings.Contains(strings.ToLower(l), "error") && !strings.Contains(strings.ToLower(l), "panic")
		case "git-log":
			ok = strings.Contains(l, "commit ") || strings.HasPrefix(l, "Author:") || strings.HasPrefix(l, "Date:") || strings.TrimSpace(l) == ""
		default:
			ok = true
		}
		if !ok {
			break
		}
		n++
	}
	return n
}
func summarize(kind string, lines []string) string {
	switch kind {
	case "diff":
		return summarizeDiffOutput(lines)
	case "test":
		return summarizeTest(lines)
	case "tree":
		return summarizeEdges("tree", lines)
	case "ls":
		return summarizeEdges("ls", lines)
	case "log":
		return summarizeEdges("terminal-log", lines)
	case "git-log":
		return fmt.Sprintf("[rtk:git-log %d line(s) elided]", len(lines))
	case "git-status":
		return fmt.Sprintf("[rtk:git-status %d line(s) elided]", len(lines))
	case "build":
		return summarizeEdges("build-output", lines)
	case "grep":
		return summarizeEdges("grep", lines)
	case "find":
		return summarizeEdges("find", lines)
	}
	return strings.Join(lines, "\n")
}
func summarizeEdges(kind string, lines []string) string {
	if len(lines) <= 7 {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:3], "\n") + fmt.Sprintf("\n[rtk:%s %d line(s) elided]\n", kind, len(lines)-6) + strings.Join(lines[len(lines)-3:], "\n")
}
func summarizeTest(lines []string) string {
	pass, skip := 0, 0
	for _, l := range lines {
		if strings.Contains(l, "SKIP") {
			skip++
		} else {
			pass++
		}
	}
	if skip > 0 {
		return fmt.Sprintf("[rtk:test-summary %d passed, %d skipped, all OK]", pass, skip)
	}
	return fmt.Sprintf("[rtk:test-summary %d passed, all OK]", pass)
}
func summarizeDiffOutput(lines []string) string {
	add, del := 0, 0
	files := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "diff --git ") {
			files++
		}
		if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") {
			add++
		}
		if strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---") {
			del++
		}
	}
	return fmt.Sprintf("[rtk:diff-summary %d file(s), +%d/-%d]", files, add, del)
}
