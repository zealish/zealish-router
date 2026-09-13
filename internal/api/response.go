package api

import (
	"encoding/json"
	"net/http"

	"github.com/zealish/zealish-router/pkg/openai"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, kind, message string) {
	writeJSON(w, status, openai.ErrorResponse{
		Error: openai.Error{Message: message, Type: kind},
	})
}
