package respond

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// SuccessResponse represents a successful API response
type SuccessResponse struct {
	Data    interface{} `json:"data,omitempty"`
	Message string      `json:"message,omitempty"`
}

// ErrorDetail implements RFC 7807 problem details
type ErrorDetail struct {
	Type     string `json:"type,omitempty"`     // A URI reference identifying the problem type
	Title    string `json:"title"`              // Short human-readable summary
	Status   int    `json:"status"`             // HTTP status code
	Detail   string `json:"detail,omitempty"`   // Human-readable explanation
	Instance string `json:"instance,omitempty"` // URI reference identifying the occurrence
}

// JSON writes a generic JSON response with proper headers
func JSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("Failed to encode JSON response", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// Success writes a success response with optional message and data
func Success(w http.ResponseWriter, status int, message string, data interface{}) {
	JSON(w, status, SuccessResponse{
		Message: message,
		Data:    data,
	})
}

// Error writes an RFC 7807 compliant error response
func Error(w http.ResponseWriter, status int, detail string) {
	problem := ErrorDetail{
		Type:   "about:blank", // Default type if none specified
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
	}

	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(problem); err != nil {
		slog.Error("Failed to encode problem response", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// ValidationError writes validation errors in problem detail format
func ValidationError(w http.ResponseWriter, errors map[string]string) {
	problem := ErrorDetail{
		Type:   "/errors/validation",
		Title:  "Validation Error",
		Status: http.StatusBadRequest,
		Detail: "One or more validation errors occurred",
	}

	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusBadRequest)

	// Include validation errors as extension members
	response := struct {
		ErrorDetail
		Errors map[string]string `json:"errors"`
	}{
		ErrorDetail: problem,
		Errors:        errors,
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Error("Failed to encode validation error response", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}
