// Package sanitize implements the Request Sanitization extension: mechanical
// text cleanup — whitespace trimming, history windowing and duplicate
// dropping. Semantic token reduction is a different concern and lives in the
// rtk package.
package sanitize

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/zealish/zealish-router/pkg/openai"
)

var (
	trailingWS = regexp.MustCompile(`[ \t]+\n`)
	manyBlank  = regexp.MustCompile(`\n{3,}`)
)

// TrimWhitespace collapses trailing spaces and runs of blank lines in every
// string-content message except system/developer prompts, which pass through
// byte-identical. It returns the message list and the bytes saved.
func TrimWhitespace(messages []openai.Message) ([]openai.Message, int) {
	var out []openai.Message
	saved := 0

	for i, m := range messages {
		text, ok := m.Text()
		if !ok || m.Role == "system" || m.Role == "developer" {
			if out != nil {
				out = append(out, m)
			}
			continue
		}
		next := trailingWS.ReplaceAllString(text, "\n")
		next = manyBlank.ReplaceAllString(next, "\n\n")
		next = strings.TrimRight(next, " \t\n")
		if next == text {
			if out != nil {
				out = append(out, m)
			}
			continue
		}
		if out == nil {
			out = append([]openai.Message(nil), messages[:i]...)
		}
		nm := m
		nm.Content, _ = json.Marshal(next)
		out = append(out, nm)
		saved += len(text) - len(next)
	}

	if out == nil {
		return messages, 0
	}
	return out, saved
}
