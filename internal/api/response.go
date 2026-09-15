package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

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
