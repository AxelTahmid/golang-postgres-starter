package server

import (
	"log/slog"
	"net/http"
)

func openAPIJSON(w http.ResponseWriter, r *http.Request) {
	data, err := OpenAPISpec.ReadFile("openapi.json")
	if err != nil {
		http.Error(w, "OpenAPI spec unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(data); err != nil {
		slog.ErrorContext(r.Context(), "failed to write OpenAPI response", "error", err)
	}
}

func openAPIUI(w http.ResponseWriter, r *http.Request) {
	data, err := OpenAPISpec.ReadFile("openapi.json")
	if err != nil {
		http.Error(w, "OpenAPI spec unavailable", http.StatusInternalServerError)
		return
	}
	scalarUIHandler("", data)(w, r)
}
