package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/zealish/zealish-router/pkg/anthropic"
	"github.com/zealish/zealish-router/pkg/openai"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeBody writes an already-serialized JSON response. Handlers that also
// cache their output encode once and reuse the bytes for both paths.
func writeBody(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func writeError(w http.ResponseWriter, status int, kind, message string) {
	writeJSON(w, status, openai.ErrorResponse{
		Error: openai.Error{Message: message, Type: kind},
	})
}

// messagesPath is the Anthropic-dialect endpoint. It is named because the
// shared middleware has to know which envelope a failure belongs in, and that
// decision is made before chi has resolved a route.
const messagesPath = "/v1/messages"

// writeGatewayError reports a failure in the dialect the endpoint speaks. A
// client that sent an Anthropic request must never be handed an OpenAI error
// body, so the shared middleware routes its errors through here.
func writeGatewayError(w http.ResponseWriter, r *http.Request, status int, kind, message string) {
	if r.URL.Path == messagesPath {
		writeAnthropicError(w, status, kind, message)
		return
	}
	writeError(w, status, kind, message)
}

// writeAnthropicError reports a failure in the envelope an Anthropic client
// expects. The dialect is picked by the endpoint, not by the failure: a client
// that speaks Messages must never be handed an OpenAI error body.
func writeAnthropicError(w http.ResponseWriter, status int, kind, message string) {
	writeJSON(w, status, anthropic.ErrorResponse{
		Type:  "error",
		Error: anthropic.Error{Type: anthropicErrorType(kind), Message: message},
	})
}

// anthropicErrorType maps the OpenAI error vocabulary the handlers use onto
// the Anthropic one, so both endpoints classify the same failure identically.
func anthropicErrorType(kind string) string {
	switch kind {
	case "invalid_request_error":
		return "invalid_request_error"
	case "rate_limit_error":
		return "rate_limit_error"
	case "insufficient_quota":
		return "billing_error"
	default:
		return "api_error"
	}
}

// writeDecodeError reports why a request body could not be decoded. An
// oversized body is a 413, everything else is a malformed-JSON 400.
func writeDecodeError(w http.ResponseWriter, err error, malformed string) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "invalid_request_error",
			fmt.Sprintf("Request body exceeds the %d byte limit.", tooLarge.Limit))
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_request_error", malformed)
}
