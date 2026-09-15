package rtk

import (
	"fmt"
	"strings"
)

// detectDiff reports the run of unified-diff lines starting at i. A diff
// begins at a "diff --git", "---"/"+++" pair or an "@@" hunk header.
func detectDiff(lines []string, i int) int {
	if !strings.HasPrefix(lines[i], "diff --git ") &&
		!strings.HasPrefix(lines[i], "@@ ") &&
		!(strings.HasPrefix(lines[i], "--- ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ ")) {
		return 0
	}
	n := 0
	for i+n < len(lines) {
		l := lines[i+n]
		if strings.HasPrefix(l, "diff --git ") ||
			strings.HasPrefix(l, "index ") ||
			strings.HasPrefix(l, "--- ") ||
			strings.HasPrefix(l, "+++ ") ||
			strings.HasPrefix(l, "@@ ") ||
			strings.HasPrefix(l, "+") ||
			strings.HasPrefix(l, "-") ||
			strings.HasPrefix(l, " ") ||
			strings.HasPrefix(l, "new file mode") ||
			strings.HasPrefix(l, "deleted file mode") ||
			strings.HasPrefix(l, "similarity index") ||
			strings.HasPrefix(l, "rename from") ||
			strings.HasPrefix(l, "rename to") ||
			strings.HasPrefix(l, `\ No newline`) {
			n++
			continue
		}
		break
	}
	return n
}

// summarizeDiff renders a per-file change summary of a unified diff: files
// touched, hunk count, added/removed line counts. The first and last hunk
// headers per file are kept so the model still knows where changes landed.
func summarizeDiff(lines []string) string {
	type fileStat struct {
		name          string
		hunks         []string
		added, remove int
	}
	var files []*fileStat
	cur := func() *fileStat {
		if len(files) == 0 {
			files = append(files, &fileStat{name: "(unknown)"})
		}
		return files[len(files)-1]
	}

	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			name := l
			if parts := strings.Fields(l); len(parts) == 4 {
				name = strings.TrimPrefix(parts[3], "b/")
			}
			files = append(files, &fileStat{name: name})
		case strings.HasPrefix(l, "+++ "):
			name := strings.TrimPrefix(strings.TrimPrefix(l, "+++ "), "b/")
			if len(files) == 0 || files[len(files)-1].name == "(unknown)" {
				files = append(files, &fileStat{name: name})
			}
		case strings.HasPrefix(l, "@@ "):
			cur().hunks = append(cur().hunks, l)
		case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++"):
			cur().added++
		case strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---"):
			cur().remove++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[rtk:diff-summary %d file(s)]\n", len(files))
	for _, f := range files {
		fmt.Fprintf(&b, "%s: %d hunk(s), +%d/-%d\n", f.name, len(f.hunks), f.added, f.remove)
		if len(f.hunks) > 0 {
			b.WriteString("  " + f.hunks[0] + "\n")
			if len(f.hunks) > 1 {
				b.WriteString("  " + f.hunks[len(f.hunks)-1] + "\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
