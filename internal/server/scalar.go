package server

import (
	"fmt"
	"log/slog"
	"net/http"

	scalar "github.com/MarceloPetrucio/go-scalar-api-reference"
)

// scalarUIHandler returns an HTTP handler that renders the Scalar API reference UI.
// Pass raw JSON spec bytes as the second argument to embed the spec inline.
// This is required for relative or auth-protected spec URLs, because the Scalar
// library only resolves http(s) URLs — anything else is treated as a local file path.
//
// It lives in internal/server so the docs infrastructure handler can embed
// the committed spec without the server binary carrying generation code.
func scalarUIHandler(specURL string, specContent ...[]byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		opts := scalar.Options{
			SpecURL: specURL,
			CustomOptions: scalar.CustomOptions{
				PageTitle: "API Documentation",
			},
			DarkMode:           true,
			ShowSidebar:        true,
			HideModels:         false,
			HideDownloadButton: false,
			Layout:             scalar.LayoutModern,
		}
		if len(specContent) > 0 && len(specContent[0]) > 0 {
			opts.SpecURL = ""
			opts.SpecContent = string(specContent[0])
		}
		htmlContent, err := scalar.ApiReferenceHTML(&opts)

		if err != nil {
			slog.ErrorContext(r.Context(), "[openapi] scalarUIHandler: failed to generate API reference HTML", "error", err)
			http.Error(w, "Failed to generate API reference", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if _, err = fmt.Fprintln(w, htmlContent); err != nil {
			// Status is already committed, so there is no error response left
			// to send — the client simply went away mid-page.
			slog.DebugContext(r.Context(), "[openapi] scalarUIHandler: failed to write API reference", "error", err)
		}
	}
}
