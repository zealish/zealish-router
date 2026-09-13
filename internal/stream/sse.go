// Package stream owns Server-Sent Event framing for streaming responses. It
// knows about HTTP writing only, never about providers or routing.
package stream

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// DoneSentinel is the final payload of an OpenAI-compatible SSE stream.
const DoneSentinel = "[DONE]"

// DefaultHeartbeat is the idle interval after which a comment frame is sent to
// keep intermediaries from closing the connection.
const DefaultHeartbeat = 15 * time.Second

// ErrUnsupported is returned when the response writer cannot flush, which makes
// streaming impossible.
var ErrUnsupported = errors.New("stream: response writer does not support flushing")

// Writer frames values as SSE events on an http.ResponseWriter.
type Writer struct {
	w       http.ResponseWriter
	flusher http.Flusher
	enc     *json.Encoder
	started bool
}

// NewWriter prepares w for SSE and returns a framing writer. It does not write
// anything until the first Event or Start call.
func NewWriter(w http.ResponseWriter) (*Writer, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, ErrUnsupported
	}
	return &Writer{w: w, flusher: flusher, enc: json.NewEncoder(w)}, nil
}

// Start emits the SSE response headers. It is idempotent and is called
// implicitly by the first Event. Until it runs, the caller may still replace
// the response with a regular error body.
func (s *Writer) Start() {
	if s.started {
		return
	}
	s.started = true

	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// Disable response buffering in nginx and compatible reverse proxies.
	h.Set("X-Accel-Buffering", "no")

	s.w.WriteHeader(http.StatusOK)
	s.flusher.Flush()
}

// Started reports whether response headers have been committed. Once true the
// status code can no longer be changed.
func (s *Writer) Started() bool { return s.started }

// Event writes v as a JSON `data:` frame and flushes it.
func (s *Writer) Event(v any) error {
	s.Start()

	if _, err := s.w.Write([]byte("data: ")); err != nil {
		return fmt.Errorf("stream: write frame: %w", err)
	}
	// Encode terminates the payload with a newline; a second one closes the event.
	if err := s.enc.Encode(v); err != nil {
		return fmt.Errorf("stream: encode payload: %w", err)
	}
	if _, err := s.w.Write([]byte("\n")); err != nil {
		return fmt.Errorf("stream: write frame: %w", err)
	}
	s.flusher.Flush()
	return nil
}

// Heartbeat writes an SSE comment frame, which clients ignore but which keeps
// proxies and load balancers from treating the connection as idle.
func (s *Writer) Heartbeat() error {
	s.Start()

	if _, err := s.w.Write([]byte(": keepalive\n\n")); err != nil {
		return fmt.Errorf("stream: write heartbeat: %w", err)
	}
	s.flusher.Flush()
	return nil
}

// Done writes the terminating [DONE] sentinel.
func (s *Writer) Done() error {
	s.Start()

	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", DoneSentinel); err != nil {
		return fmt.Errorf("stream: write sentinel: %w", err)
	}
	s.flusher.Flush()
	return nil
}

// Error writes an error envelope as a final data frame followed by the
// sentinel. It is only meaningful after headers are committed, since the status
// code can no longer convey the failure.
func (s *Writer) Error(payload any) error {
	if err := s.Event(payload); err != nil {
		return err
	}
	return s.Done()
}
