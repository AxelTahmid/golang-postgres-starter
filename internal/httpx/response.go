package httpx

import (
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
)

// Response represents a successful HTTP response.
type Response struct {
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
	Meta    *Meta       `json:"meta,omitempty"`
}

// Meta provides additional response metadata (e.g., pagination).
type Meta struct {
	Page       int `json:"page,omitempty"`
	PageSize   int `json:"page_size,omitempty"`
	TotalPages int `json:"total_pages,omitempty"`
	TotalItems int `json:"total_items,omitempty"`
}

// Success sends a successful JSON response to the client.
// It encodes the response directly to the response writer.
func Success(w http.ResponseWriter, code int, msg string, data interface{}, meta *Meta) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)

	resp := Response{
		Message: msg,
		Data:    data,
		Meta:    meta,
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		// If encoding fails after writing headers, we log the error.
		log.Printf("Failed to encode success response: %v", err)
	}
}

// Error sends an error response in the "application/problem+json" format.
// If problem is nil, a default problem detail is generated.
func Error(w http.ResponseWriter, status int, msg string) {
	problem := NewProblemDetails(
		status,
		msg,
		http.StatusText(status),
		"",
	)

	writeProblem(w, problem)
}

func writeProblem(w http.ResponseWriter, problem *ProblemDetails) {
	if problem == nil {
		problem = NewProblemDetails(
			500,
			GetProblemTypeURL("invalid_request"),
			"Invalid Request",
			"An unexpected error occurred",
		)
	}

	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.WriteHeader(problem.Status)

	if err := json.NewEncoder(w).Encode(problem); err != nil {
		slog.Error("Failed to encode problem response", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}
