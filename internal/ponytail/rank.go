package ponytail

import "math"

// scoredMessage pairs a message with its relevance score and original index.
type scoredMessage struct {
	message Message
	index   int
	score   float64
}

// rankMessages assigns a relevance score to every message. Higher scores
// mean higher priority for retention.
//
// Scoring rules (descending priority):
//   - system messages: 100
//   - developer messages: 90
//   - latest user message: 80
//   - active tool results (role=tool): 70
//   - recent assistant messages (last 2): 60
//   - older conversation: score decreases linearly with age from 40 → 10
func rankMessages(messages []Message) []scoredMessage {
	n := len(messages)
	scored := make([]scoredMessage, n)

	// Find the index of the latest user message.
	lastUserIdx := -1
	for i := n - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			lastUserIdx = i
			break
		}
	}

	for i, m := range messages {
		scored[i] = scoredMessage{message: m, index: i}
		switch m.Role {
		case "system":
			scored[i].score = 100
		case "developer":
			scored[i].score = 90
		case "tool":
			scored[i].score = 70
		case "user":
			if i == lastUserIdx {
				scored[i].score = 80
			} else {
				scored[i].score = ageScore(i, n)
			}
		case "assistant":
			// Last 2 assistant messages get elevated score.
			if isRecentAssistant(messages, i, 2) {
				scored[i].score = 60
			} else {
				scored[i].score = ageScore(i, n)
			}
		default:
			scored[i].score = ageScore(i, n)
		}
	}
	return scored
}

// ageScore returns a score between 10 and 40 based on message position.
// Earlier messages score lower; the first message gets 10, the last gets 40.
func ageScore(index, total int) float64 {
	if total <= 1 {
		return 40
	}
	return 10 + 30*float64(index)/float64(total-1)
}

// isRecentAssistant reports whether the assistant message at idx is among
// the last `count` assistant messages in the conversation.
func isRecentAssistant(messages []Message, idx, count int) bool {
	seen := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			seen++
			if i == idx {
				return seen <= count
			}
		}
	}
	return false
}

// filterByBudget keeps all protected messages plus the highest-scored
// remaining messages that fit within tokenBudget. Messages are returned in
// their original order.
func filterByBudget(messages []Message, scored []scoredMessage, tokenBudget int, protected int) []Message {
	n := len(messages)
	keep := make([]bool, n)

	// Protect the last `protected` messages (the tail of the conversation).
	for i := max(0, n-protected); i < n; i++ {
		keep[i] = true
	}

	// Count tokens already committed by protected messages.
	used := 0
	for i := 0; i < n; i++ {
		if keep[i] {
			used += estimateTokensFromMessages([]Message{messages[i]})
		}
	}

	// Build candidate list: non-protected messages, sorted by score descending.
	candidates := make([]scoredMessage, 0, n)
	for _, s := range scored {
		if !keep[s.index] {
			candidates = append(candidates, s)
		}
	}
	sortScored(candidates)

	// Greedily add highest-scored messages until budget is exhausted.
	for _, c := range candidates {
		cost := estimateTokensFromMessages([]Message{c.message})
		if used+cost > tokenBudget {
			continue
		}
		keep[c.index] = true
		used += cost
	}

	// Rebuild in original order.
	result := make([]Message, 0, n)
	for i, m := range messages {
		if keep[i] {
			result = append(result, m)
		}
	}
	return result
}

// sortScored sorts scored messages by score descending (highest first).
func sortScored(s []scoredMessage) {
	// Simple insertion sort — the slice is small (conversation length).
	for i := 1; i < len(s); i++ {
		key := s[i]
		j := i - 1
		for j >= 0 && s[j].score < key.score {
			s[j+1] = s[j]
			j--
		}
		s[j+1] = key
	}
}

// max returns the larger of a and b.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// minFloat returns the smaller of a and b.
func minFloat(a, b float64) float64 {
	return math.Min(a, b)
}
