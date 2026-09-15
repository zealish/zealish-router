package api

import (
	"context"
	"encoding/json"
	"time"

	"github.com/zealish/zealish-router/internal/stream"
	"github.com/zealish/zealish-router/pkg/openai"
)

// The Responses dialect streams named, sequence-numbered events that bracket
// the response and each output item:
//
//	response.created → (response.output_item.added →
//	response.output_text.delta* | response.function_call_arguments.delta* →
//	response.output_item.done)* → response.completed
//
// There is no [DONE] sentinel: response.completed is the terminator.
// responseStream is the state machine that turns uniform Chat Completions
// chunks into that sequence. It is driven by relayResponses and owns no
// transport concerns beyond the writer.
type responseStream struct {
	w   *stream.Writer
	req *openai.ResponseRequest

	id      string
	model   string
	created int64
	seq     int

	started bool
	// itemOpen tracks the in-flight output item. Text and function calls
	// never share one, so a switch closes the open item first.
	itemOpen  bool
	itemIndex int
	opened    bool
	item      openai.ResponseItem
	text      []byte
	args      []byte
	// toolItems maps a Chat Completions tool_call index to the output item it
	// was opened as, so argument fragments land in the right item.
	toolItems map[int]int
	// done collects the completed items, so response.completed can carry
	// the full aggregated output an SDK reads final state from.
	done []openai.ResponseItem

	finish string
	usage  *openai.Usage
}

func newResponseStream(w *stream.Writer, req *openai.ResponseRequest) *responseStream {
	return &responseStream{w: w, req: req, model: req.Model, toolItems: make(map[int]int)}
}

// relayResponses forwards a Chat Completions chunk channel to a Responses
// client. It mirrors stream.Relay but terminates with response.completed
// instead of the [DONE] sentinel, which is not part of this dialect.
func relayResponses(ctx context.Context, w *stream.Writer, req *openai.ResponseRequest, chunks <-chan openai.StreamChunk, heartbeat time.Duration) error {
	var idle <-chan time.Time
	if heartbeat > 0 {
		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()
		idle = ticker.C
	}

	rs := newResponseStream(w, req)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case chunk, ok := <-chunks:
			if !ok {
				return rs.finishStream()
			}
			if err := rs.chunk(chunk); err != nil {
				return err
			}

		case <-idle:
			if err := w.Heartbeat(); err != nil {
				return err
			}
		}
	}
}

// event frames one named event, stamping the sequence number every event of
// this dialect carries.
func (m *responseStream) event(name string, payload map[string]any) error {
	m.seq++
	payload["type"] = name
	payload["sequence_number"] = m.seq
	return m.w.NamedEvent(name, payload)
}

// chunk translates one Chat Completions chunk into the events it implies.
func (m *responseStream) chunk(c openai.StreamChunk) error {
	if c.Usage != nil {
		m.usage = c.Usage
	}
	if err := m.start(c); err != nil {
		return err
	}

	for _, choice := range c.Choices {
		if choice.Delta != nil {
			if err := m.delta(*choice.Delta); err != nil {
				return err
			}
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			m.finish = *choice.FinishReason
		}
	}
	return nil
}

// start emits response.created and response.in_progress once, carrying
// whatever identity the first chunk reported.
func (m *responseStream) start(c openai.StreamChunk) error {
	if m.started {
		return nil
	}
	m.started = true
	m.id = responseID(c.ID)
	m.created = c.Created
	if c.Model != "" {
		m.model = c.Model
	}

	snapshot := m.snapshot("in_progress", nil)
	if err := m.event("response.created", map[string]any{"response": snapshot}); err != nil {
		return err
	}
	return m.event("response.in_progress", map[string]any{"response": snapshot})
}

// snapshot builds the response envelope the lifecycle events carry.
func (m *responseStream) snapshot(status string, output []openai.ResponseItem) *openai.Response {
	if output == nil {
		output = []openai.ResponseItem{}
	}
	out := &openai.Response{
		ID:              m.id,
		Object:          "response",
		CreatedAt:       m.created,
		Status:          status,
		Model:           m.model,
		Output:          output,
		Instructions:    m.req.Instructions,
		MaxOutputTokens: m.req.MaxOutputTokens,
		Temperature:     m.req.Temperature,
		TopP:            m.req.TopP,
		Tools:           m.req.Tools,
		ToolChoice:      m.req.ToolChoice,
	}
	if out.Tools == nil {
		out.Tools = []openai.ResponseTool{}
	}
	return out
}

// delta translates the incremental payload of one choice.
func (m *responseStream) delta(d openai.Message) error {
	if text, ok := d.Text(); ok && text != "" {
		if err := m.openTextItem(); err != nil {
			return err
		}
		m.text = append(m.text, text...)
		if err := m.event("response.output_text.delta", map[string]any{
			"item_id":       m.item.ID,
			"output_index":  m.itemIndex,
			"content_index": 0,
			"delta":         text,
		}); err != nil {
			return err
		}
	}
	return m.toolDeltas(d.Extra["tool_calls"])
}

// openTextItem starts a message item unless one is already open. A text delta
// arriving after a function call closes the call first, since items do not
// interleave.
func (m *responseStream) openTextItem() error {
	if m.itemOpen && m.item.Type == "message" {
		return nil
	}
	if err := m.closeItem(); err != nil {
		return err
	}
	m.advance()
	m.item = openai.ResponseItem{
		Type:   "message",
		ID:     "msg_" + m.id,
		Role:   "assistant",
		Status: "in_progress",
	}
	m.text = nil
	return m.itemAdded()
}

// toolDeltas translates the tool_calls fragments of a delta. A call announces
// itself with an id and name, then streams its arguments as partial JSON.
func (m *responseStream) toolDeltas(raw []byte) error {
	calls := decodeStreamToolCalls(raw)
	for _, call := range calls {
		idx := 0
		if call.Index != nil {
			idx = *call.Index
		}

		if _, open := m.toolItems[idx]; !open {
			if err := m.closeItem(); err != nil {
				return err
			}
			m.advance()
			m.item = openai.ResponseItem{
				Type:   "function_call",
				ID:     "fc_" + m.id,
				CallID: call.ID,
				Name:   call.Function.Name,
				Status: "in_progress",
			}
			m.args = nil
			m.toolItems[idx] = m.itemIndex
			if err := m.itemAdded(); err != nil {
				return err
			}
		}
		if call.Function.Arguments == "" {
			continue
		}
		m.args = append(m.args, call.Function.Arguments...)
		if err := m.event("response.function_call_arguments.delta", map[string]any{
			"item_id":      m.item.ID,
			"output_index": m.toolItems[idx],
			"delta":        call.Function.Arguments,
		}); err != nil {
			return err
		}
	}
	return nil
}

// decodeStreamToolCalls parses the tool_calls array of a streaming delta.
func decodeStreamToolCalls(raw []byte) []streamToolCall {
	if len(raw) == 0 {
		return nil
	}
	var calls []streamToolCall
	if err := json.Unmarshal(raw, &calls); err != nil {
		return nil
	}
	return calls
}

// advance moves to the next output index; the first item takes index 0.
func (m *responseStream) advance() {
	if m.opened {
		m.itemIndex++
	}
	m.opened = true
	m.itemOpen = true
}

// itemAdded announces the item the next deltas belong to.
func (m *responseStream) itemAdded() error {
	return m.event("response.output_item.added", map[string]any{
		"output_index": m.itemIndex,
		"item":         m.item,
	})
}

// closeItem finishes the item in flight: the accumulated text or arguments
// are reported once complete, then the item is marked done.
func (m *responseStream) closeItem() error {
	if !m.itemOpen {
		return nil
	}
	m.itemOpen = false

	switch m.item.Type {
	case "message":
		if err := m.event("response.output_text.done", map[string]any{
			"item_id":       m.item.ID,
			"output_index":  m.itemIndex,
			"content_index": 0,
			"text":          string(m.text),
		}); err != nil {
			return err
		}
		m.item.Status = "completed"
		m.item.Content = mustMarshal([]openai.ResponseContentPart{{
			Type:        "output_text",
			Text:        string(m.text),
			Annotations: []json.RawMessage{},
		}})
	case "function_call":
		args := argumentsOrEmpty(string(m.args))
		if err := m.event("response.function_call_arguments.done", map[string]any{
			"item_id":      m.item.ID,
			"output_index": m.itemIndex,
			"arguments":    args,
		}); err != nil {
			return err
		}
		m.item.Status = "completed"
		m.item.Arguments = args
	}

	if err := m.event("response.output_item.done", map[string]any{
		"output_index": m.itemIndex,
		"item":         m.item,
	}); err != nil {
		return err
	}
	m.done = append(m.done, m.item)
	return nil
}

// finishStream closes the response. response.completed carries the final
// envelope with the aggregated output and usage, which is where an SDK reads
// them from. There is no [DONE] sentinel in this dialect.
func (m *responseStream) finishStream() error {
	if !m.started {
		// The upstream closed without a single chunk. A client still needs a
		// well-formed response rather than an empty body.
		if err := m.start(openai.StreamChunk{Model: m.model}); err != nil {
			return err
		}
	}

	if m.itemOpen {
		if err := m.closeItem(); err != nil {
			return err
		}
	}

	final := m.snapshot("completed", m.done)
	final.Usage = toResponseUsage(m.usage)
	if m.finish == "length" {
		final.Status = "incomplete"
		final.IncompleteDetails = &openai.IncompleteDetails{Reason: "max_output_tokens"}
		return m.event("response.incomplete", map[string]any{"response": final})
	}
	return m.event("response.completed", map[string]any{"response": final})
}
