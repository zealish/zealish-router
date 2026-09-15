package api

import (
	"context"
	"encoding/json"
	"time"

	"github.com/zealish/zealish-router/internal/stream"
	"github.com/zealish/zealish-router/pkg/anthropic"
	"github.com/zealish/zealish-router/pkg/openai"
)

// The two dialects frame a stream differently. OpenAI sends one chunk shape
// throughout and terminates with a [DONE] sentinel; Anthropic sends named
// events that bracket the message and each content block:
//
//	message_start → (content_block_start → content_block_delta* →
//	content_block_stop)* → message_delta → message_stop
//
// messageStream is the state machine that turns the former into the latter. It
// is driven by relayMessages and owns no transport concerns beyond the writer.
type messageStream struct {
	w     *stream.Writer
	id    string
	model string

	started bool
	// opened records that at least one content block has been emitted, so the
	// first block takes index 0 and every later one increments.
	opened     bool
	blockOpen  bool
	blockKind  string
	blockIndex int
	// toolBlocks maps an OpenAI tool_call index to the Anthropic content block
	// index it was opened as, so argument fragments land in the right block.
	toolBlocks map[int]int

	stopReason string
	usage      *openai.Usage
}

// streamToolCall is one entry of a streaming delta's tool_calls array.
type streamToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

func newMessageStream(w *stream.Writer, model string) *messageStream {
	return &messageStream{w: w, model: model, toolBlocks: make(map[int]int)}
}

// relayMessages forwards an OpenAI chunk channel to an Anthropic client. It
// mirrors stream.Relay but closes the message with message_stop instead of the
// [DONE] sentinel, which is not part of this dialect.
func relayMessages(ctx context.Context, w *stream.Writer, model string, chunks <-chan openai.StreamChunk, heartbeat time.Duration) error {
	var idle <-chan time.Time
	if heartbeat > 0 {
		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()
		idle = ticker.C
	}

	ms := newMessageStream(w, model)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case chunk, ok := <-chunks:
			if !ok {
				return ms.finish()
			}
			if err := ms.chunk(chunk); err != nil {
				return err
			}

		case <-idle:
			// Anthropic models keepalives as a named ping rather than a
			// comment frame.
			if err := w.NamedEvent("ping", map[string]string{"type": "ping"}); err != nil {
				return err
			}
		}
	}
}

// chunk translates one OpenAI chunk into the events it implies.
func (m *messageStream) chunk(c openai.StreamChunk) error {
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
			m.stopReason = stopReason(*choice.FinishReason)
		}
	}
	return nil
}

// start emits message_start once, carrying whatever identity the first chunk
// reported. Input tokens are only known here on upstreams that report usage
// up front; the rest report it at the end and it is repeated on message_delta.
func (m *messageStream) start(c openai.StreamChunk) error {
	if m.started {
		return nil
	}
	m.started = true
	m.id = c.ID
	if c.Model != "" {
		m.model = c.Model
	}

	return m.w.NamedEvent("message_start", map[string]any{
		"type": "message_start",
		"message": anthropic.Response{
			ID:      m.id,
			Type:    "message",
			Role:    "assistant",
			Model:   m.model,
			Content: []anthropic.ContentBlock{},
			Usage:   &anthropic.Usage{},
		},
	})
}

// delta translates the incremental payload of one choice.
func (m *messageStream) delta(d openai.Message) error {
	if text, ok := d.Text(); ok && text != "" {
		// An SDK concatenates onto this, so the empty string must be present.
		if err := m.openBlock("text", map[string]string{"type": "text", "text": ""}); err != nil {
			return err
		}
		if err := m.w.NamedEvent("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": m.blockIndex,
			"delta": anthropic.Delta{Type: "text_delta", Text: text},
		}); err != nil {
			return err
		}
	}
	return m.toolDeltas(d.Extra["tool_calls"])
}

// toolDeltas translates the tool_calls fragments of a delta. A call announces
// itself with an id and name, then streams its arguments as partial JSON.
func (m *messageStream) toolDeltas(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var calls []streamToolCall
	if err := json.Unmarshal(raw, &calls); err != nil {
		return nil
	}

	for _, call := range calls {
		idx := 0
		if call.Index != nil {
			idx = *call.Index
		}

		if _, open := m.toolBlocks[idx]; !open {
			if err := m.openBlock("tool_use", anthropic.ContentBlock{
				Type:  "tool_use",
				ID:    call.ID,
				Name:  call.Function.Name,
				Input: json.RawMessage(`{}`),
			}); err != nil {
				return err
			}
			m.toolBlocks[idx] = m.blockIndex
		}
		if call.Function.Arguments == "" {
			continue
		}
		if err := m.w.NamedEvent("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": m.toolBlocks[idx],
			"delta": anthropic.Delta{Type: "input_json_delta", PartialJSON: call.Function.Arguments},
		}); err != nil {
			return err
		}
	}
	return nil
}

// openBlock starts a content block unless one of the same kind is already open.
// A text delta arriving after a tool block — or the other way round — closes
// the current block first, since Anthropic blocks do not interleave.
//
// The opening payload is passed in rather than built here: a text block must
// serialise an empty "text" that ContentBlock omits, and the two kinds share
// no other fields.
func (m *messageStream) openBlock(kind string, block any) error {
	if m.blockOpen && m.blockKind == kind && kind == "text" {
		return nil
	}
	if err := m.closeBlock(); err != nil {
		return err
	}
	if m.opened {
		m.blockIndex++
	}
	m.opened = true
	m.blockOpen = true
	m.blockKind = kind

	return m.w.NamedEvent("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         m.blockIndex,
		"content_block": block,
	})
}

// closeBlock emits content_block_stop for the block in flight, if any.
func (m *messageStream) closeBlock() error {
	if !m.blockOpen {
		return nil
	}
	m.blockOpen = false
	return m.w.NamedEvent("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": m.blockIndex,
	})
}

// finish closes the message. message_delta carries the stop reason and the
// output token count, which is where an Anthropic client reads usage from.
func (m *messageStream) finish() error {
	if !m.started {
		// The upstream closed without a single chunk. A client still needs a
		// well-formed message rather than an empty body.
		if err := m.start(openai.StreamChunk{Model: m.model}); err != nil {
			return err
		}
	}
	if err := m.closeBlock(); err != nil {
		return err
	}

	reason := m.stopReason
	if reason == "" {
		reason = "end_turn"
	}
	usage := toAnthropicUsage(m.usage)
	if usage == nil {
		usage = &anthropic.Usage{}
	}
	if err := m.w.NamedEvent("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": reason, "stop_sequence": nil},
		"usage": usage,
	}); err != nil {
		return err
	}
	return m.w.NamedEvent("message_stop", map[string]string{"type": "message_stop"})
}
