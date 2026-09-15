package stream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type chunk struct {
	ID string `json:"id"`
}

// unflushable is a ResponseWriter without a Flush method.
type unflushable struct{ http.ResponseWriter }

func TestNewWriterRequiresFlusher(t *testing.T) {
	if _, err := NewWriter(unflushable{httptest.NewRecorder()}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestWriterHeadersAndFraming(t *testing.T) {
	rec := httptest.NewRecorder()
	w, err := NewWriter(rec)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if w.Started() {
		t.Error("headers must not be committed before the first write")
	}

	for _, id := range []string{"a", "b"} {
		if err := w.Event(chunk{ID: id}); err != nil {
			t.Fatalf("Event(%s): %v", id, err)
		}
	}
	if err := w.Done(); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if !w.Started() {
		t.Error("Started() should be true after writing")
	}

	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q, want no", got)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}

	want := "data: {\"id\":\"a\"}\n\ndata: {\"id\":\"b\"}\n\ndata: [DONE]\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestWriterHeartbeatIsComment(t *testing.T) {
	rec := httptest.NewRecorder()
	w, _ := NewWriter(rec)

	if err := w.Heartbeat(); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if got := rec.Body.String(); !strings.HasPrefix(got, ":") {
		t.Fatalf("heartbeat = %q, want an SSE comment frame", got)
	}
}

func TestWriterNamedEventPrefixesEventLine(t *testing.T) {
	rec := httptest.NewRecorder()
	w, _ := NewWriter(rec)

	if err := w.NamedEvent("message_stop", chunk{ID: "a"}); err != nil {
		t.Fatalf("NamedEvent: %v", err)
	}
	want := "event: message_stop\ndata: {\"id\":\"a\"}\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestWriterNamedEventWithoutNameIsPlainFrame(t *testing.T) {
	rec := httptest.NewRecorder()
	w, _ := NewWriter(rec)

	if err := w.NamedEvent("", chunk{ID: "a"}); err != nil {
		t.Fatalf("NamedEvent: %v", err)
	}
	if got := rec.Body.String(); got != "data: {\"id\":\"a\"}\n\n" {
		t.Fatalf("body = %q, want an unnamed frame", got)
	}
}

func TestRelayForwardsAndTerminates(t *testing.T) {
	rec := httptest.NewRecorder()
	w, _ := NewWriter(rec)

	chunks := make(chan chunk, 2)
	chunks <- chunk{ID: "a"}
	chunks <- chunk{ID: "b"}
	close(chunks)

	if err := Relay(context.Background(), w, chunks, 0); err != nil {
		t.Fatalf("Relay: %v", err)
	}

	body := rec.Body.String()
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Errorf("stream not terminated with the sentinel: %q", body)
	}
	if n := strings.Count(body, "data: "); n != 3 {
		t.Errorf("data frames = %d, want 3 (2 chunks + sentinel)", n)
	}
}

func TestRelayStopsOnContextCancel(t *testing.T) {
	rec := httptest.NewRecorder()
	w, _ := NewWriter(rec)

	chunks := make(chan chunk) // never written to, never closed
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- Relay(ctx, w, chunks, 0) }()

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Relay did not return after cancellation")
	}
	if strings.Contains(rec.Body.String(), DoneSentinel) {
		t.Error("a disconnected client must not receive the sentinel")
	}
}

func TestRelayEmitsHeartbeatWhenIdle(t *testing.T) {
	rec := httptest.NewRecorder()
	w, _ := NewWriter(rec)

	chunks := make(chan chunk)
	go func() {
		time.Sleep(60 * time.Millisecond)
		close(chunks)
	}()

	if err := Relay(context.Background(), w, chunks, 10*time.Millisecond); err != nil {
		t.Fatalf("Relay: %v", err)
	}
	if !strings.Contains(rec.Body.String(), ": keepalive") {
		t.Fatalf("no heartbeat emitted while idle: %q", rec.Body.String())
	}
}
