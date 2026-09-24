package ponytail

import (
	"encoding/json"
	"strings"
)

// compressConversation applies heuristic compression to messages outside the
// protected window. Messages inside the window are returned untouched.
//
// Mode controls the aggressiveness:
//   - conservative: minimal trimming of very long messages only
//   - balanced: truncate older messages to first+last sentences
//   - aggressive: reduce older messages to bullet-point summaries
func compressConversation(messages []Message, protectedWindow int, mode string) []Message {
	if len(messages) == 0 {
		return messages
	}

	n := len(messages)
	cutoff := max(0, n-protectedWindow)

	result := make([]Message, len(messages))
	copy(result, messages)

	for i := 0; i < cutoff; i++ {
		text, ok := extractMessageText(messages[i])
		if !ok || text == "" {
			continue
		}
		compressed := compressText(text, mode)
		if compressed != text {
			result[i].Content = marshalText(compressed)
		}
	}
	return result
}

// compressText applies text-level compression based on mode.
func compressText(text, mode string) string {
	switch mode {
	case "aggressive":
		return compressAggressive(text)
	case "balanced":
		return compressBalanced(text)
	default: // conservative
		return compressConservative(text)
	}
}

// compressConservative only trims very long messages by removing redundant
// whitespace and truncating if excessively long.
func compressConservative(text string) string {
	// Remove runs of 3+ blank lines.
	text = collapseBlankLines(text, 2)
	// Truncate very long messages (>2000 chars) to first 1500 + note.
	if len(text) > 2000 {
		text = text[:1500] + "\n\n[...truncated for context optimization...]"
	}
	return text
}

// compressBalanced truncates older messages to first and last sentences,
// removing blank lines and redundant whitespace.
func compressBalanced(text string) string {
	text = collapseBlankLines(text, 1)

	// If short enough, leave as-is.
	if len(text) <= 500 {
		return text
	}

	sentences := splitSentences(text)
	if len(sentences) <= 2 {
		return text
	}

	// Keep first 2 and last sentence.
	var b strings.Builder
	b.WriteString(sentences[0])
	b.WriteString(" ")
	b.WriteString(sentences[1])
	b.WriteString("\n[...]\n")
	b.WriteString(sentences[len(sentences)-1])
	return b.String()
}

// compressAggressive reduces older messages to bullet-point extraction of
// key content, removing all non-essential structure.
func compressAggressive(text string) string {
	text = collapseBlankLines(text, 0)

	if len(text) <= 200 {
		return text
	}

	lines := strings.Split(text, "\n")
	var bullets []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Keep lines that look like bullet points, headers, or key statements.
		if isKeyLine(line) {
			bullets = append(bullets, "- "+line)
		}
	}

	if len(bullets) == 0 {
		// Fallback: keep first and last sentence.
		sentences := splitSentences(text)
		if len(sentences) > 1 {
			return sentences[0] + " [...] " + sentences[len(sentences)-1]
		}
		return text[:min(200, len(text))]
	}

	result := strings.Join(bullets, "\n")
	if len(result) > 600 {
		result = result[:600] + "\n- [...truncated...]"
	}
	return result
}

// compressCodeBlocks reduces code verbosity while preserving structure.
// It operates on a text string that may contain fenced code blocks.
//
// Preservation rules:
//   - imports, exports, function signatures, types, interfaces
//   - comments marked IMPORTANT
//
// Removal rules:
//   - repeated blank lines
//   - duplicated comments
//   - formatting noise
//
// Mode controls aggressiveness:
//   - conservative: only remove blank lines
//   - balanced: remove blank lines + non-IMPORTANT comments
//   - aggressive: only keep signatures + IMPORTANT comments
func compressCodeBlocks(text, mode string) string {
	// Split text into code blocks and non-code sections.
	sections := splitCodeSections(text)
	var b strings.Builder
	for _, sec := range sections {
		if sec.isCode {
			b.WriteString(compressCode(sec.text, mode))
		} else {
			b.WriteString(sec.text)
		}
	}
	return b.String()
}

// codeSection represents a section of text that is either inside or outside
// a fenced code block.
type codeSection struct {
	text   string
	isCode bool
}

// splitCodeSections breaks text into alternating code/non-code sections
// based on ``` fences.
func splitCodeSections(text string) []codeSection {
	var sections []codeSection
	var current strings.Builder
	inCode := false

	lines := strings.Split(text, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inCode {
				// End of code block.
				current.WriteString(line)
				current.WriteString("\n")
				sections = append(sections, codeSection{text: current.String(), isCode: true})
				current.Reset()
				inCode = false
			} else {
				// Flush non-code text.
				if current.Len() > 0 {
					sections = append(sections, codeSection{text: current.String(), isCode: false})
					current.Reset()
				}
				current.WriteString(line)
				current.WriteString("\n")
				inCode = true
			}
			continue
		}
		current.WriteString(line)
		current.WriteString("\n")
	}

	// Flush remaining text.
	if current.Len() > 0 {
		sections = append(sections, codeSection{text: current.String(), isCode: inCode})
	}
	return sections
}

// compressCode applies mode-specific compression to a code block (including
// its ``` fences).
func compressCode(text, mode string) string {
	lines := strings.Split(text, "\n")
	if len(lines) <= 2 {
		return text
	}

	// Preserve the opening and closing fence lines.
	openFence := lines[0]
	closeFence := ""
	bodyLines := lines[1:]
	if len(bodyLines) > 0 && strings.TrimSpace(bodyLines[len(bodyLines)-1]) == "```" {
		closeFence = bodyLines[len(bodyLines)-1]
		bodyLines = bodyLines[:len(bodyLines)-1]
	}

	var filtered []string
	switch mode {
	case "conservative":
		filtered = filterConservative(bodyLines)
	case "balanced":
		filtered = filterBalanced(bodyLines)
	case "aggressive":
		filtered = filterAggressive(bodyLines)
	default:
		filtered = filterConservative(bodyLines)
	}

	var b strings.Builder
	b.WriteString(openFence)
	b.WriteString("\n")
	for _, line := range filtered {
		b.WriteString(line)
		b.WriteString("\n")
	}
	if closeFence != "" {
		b.WriteString(closeFence)
	}
	return b.String()
}

// filterConservative only removes blank lines.
func filterConservative(lines []string) []string {
	return collapseLineBlanks(lines, 1)
}

// filterBalanced removes blank lines and non-IMPORTANT comments.
func filterBalanced(lines []string) []string {
	result := make([]string, 0, len(lines))
	prevBlank := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if prevBlank {
				continue
			}
			prevBlank = true
			result = append(result, line)
			continue
		}
		prevBlank = false
		// Remove comments that don't contain IMPORTANT.
		if isComment(trimmed) && !strings.Contains(strings.ToUpper(trimmed), "IMPORTANT") {
			continue
		}
		result = append(result, line)
	}
	return result
}

// filterAggressive keeps only signatures, imports/exports, and IMPORTANT comments.
func filterAggressive(lines []string) []string {
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if isSignatureLine(trimmed) || isImportLine(trimmed) || isImportantComment(trimmed) {
			result = append(result, line)
		}
	}
	return result
}

// isComment reports whether a line looks like a code comment.
func isComment(line string) bool {
	return strings.HasPrefix(line, "//") || strings.HasPrefix(line, "#") ||
		strings.HasPrefix(line, "/*") || strings.HasPrefix(line, "*") ||
		strings.HasPrefix(line, "<!--")
}

// isImportantComment reports whether a comment is marked IMPORTANT.
func isImportantComment(line string) bool {
	return isComment(line) && strings.Contains(strings.ToUpper(line), "IMPORTANT")
}

// isSignatureLine reports whether a line is a function/type/interface/method
// signature. This is a heuristic — it catches common Go, JS/TS, Python, and
// Rust patterns.
func isSignatureLine(line string) bool {
	lower := strings.ToLower(line)
	keywords := []string{
		"func ", "function ", "async function ", "def ", "class ", "interface ",
		"type ", "struct ", "enum ", "impl ", "pub fn ", "pub async fn ",
		"export ", "export default ", "export async ",
		"public ", "private ", "protected ",
	}
	for _, kw := range keywords {
		if strings.HasPrefix(lower, kw) {
			return true
		}
	}
	// Lines ending with { or : (opening blocks) are likely signatures.
	if strings.HasSuffix(line, "{") || strings.HasSuffix(line, ":") {
		return true
	}
	return false
}

// isImportLine reports whether a line is an import/require/include statement.
func isImportLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(lower, "import ") || strings.HasPrefix(lower, "from ") ||
		strings.HasPrefix(lower, "require(") || strings.HasPrefix(lower, "#include") ||
		strings.HasPrefix(lower, "use ") || strings.HasPrefix(lower, "package ")
}

// isKeyLine reports whether a line is worth keeping in aggressive mode.
func isKeyLine(line string) bool {
	// Bullet points, numbered lists, headers.
	if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") ||
		strings.HasPrefix(line, "#") || (len(line) > 2 && line[0] >= '1' && line[0] <= '9' && line[1] == '.') {
		return true
	}
	// Lines with key indicators.
	upper := strings.ToUpper(line)
	keywords := []string{"IMPORTANT", "NOTE", "WARNING", "ERROR", "FIXME", "TODO", "HACK", "BUG"}
	for _, kw := range keywords {
		if strings.Contains(upper, kw) {
			return true
		}
	}
	return false
}

// collapseBlankLines reduces runs of blank lines to at most `keep` blank lines.
func collapseBlankLines(text string, keep int) string {
	lines := strings.Split(text, "\n")
	lines = collapseLineBlanks(lines, keep)
	return strings.Join(lines, "\n")
}

// collapseLineBlanks reduces runs of blank lines in a slice to at most `keep`.
func collapseLineBlanks(lines []string, keep int) []string {
	result := make([]string, 0, len(lines))
	blankCount := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			blankCount++
			if blankCount <= keep {
				result = append(result, line)
			}
		} else {
			blankCount = 0
			result = append(result, line)
		}
	}
	return result
}

// splitSentences splits text into sentences. Uses period/question/exclamation
// followed by whitespace as delimiters.
func splitSentences(text string) []string {
	// Simple sentence splitter — not perfect but sufficient for compression.
	var sentences []string
	current := strings.Builder{}
	runes := []rune(text)

	for i, r := range runes {
		current.WriteRune(r)
		if r == '.' || r == '!' || r == '?' {
			// Check if next rune is whitespace or end of string.
			if i+1 >= len(runes) || runes[i+1] == ' ' || runes[i+1] == '\n' || runes[i+1] == '\r' {
				s := strings.TrimSpace(current.String())
				if s != "" {
					sentences = append(sentences, s)
				}
				current.Reset()
			}
		}
	}
	if current.Len() > 0 {
		s := strings.TrimSpace(current.String())
		if s != "" {
			sentences = append(sentences, s)
		}
	}
	return sentences
}

// extractMessageText returns the text content of a message and whether it was
// a plain string.
func extractMessageText(m Message) (string, bool) {
	if len(m.Content) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return s, true
	}
	// Array of parts.
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(m.Content, &parts); err != nil {
		return "", false
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String(), true
}

// marshalText encodes a string as a JSON string for use as message content.
func marshalText(text string) json.RawMessage {
	b, _ := json.Marshal(text)
	return b
}

// min returns the smaller of a and b.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
