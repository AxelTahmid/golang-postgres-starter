package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/AxelTahmid/tinker/config"
	"github.com/AxelTahmid/tinker/internal/httpx"
	"github.com/AxelTahmid/tinker/internal/server"
)

const specPath = "internal/server/openapi.json"

func main() {
	conf := config.DocsConfig()
	app := server.RouterForDocs(conf)
	cfg := conf.OpenAPI
	out, err := app.OpenAPI(openAPIInfo(&cfg))
	if err != nil {
		slog.Error("failed to generate OpenAPI document", "error", err)
		os.Exit(1)
	}
	if err := writeFileAtomic(specPath, out); err != nil {
		slog.Error("failed to write OpenAPI document", "error", err)
		os.Exit(1)
	}
}

func openAPIInfo(c *config.OpenAPI) httpx.Info {
	var contact *httpx.Contact
	if c.Contact != nil {
		contact = &httpx.Contact{Name: c.Contact.Name, URL: c.Contact.URL, Email: c.Contact.Email}
	}
	var license *httpx.License
	if c.License != nil {
		license = &httpx.License{Name: c.License.Name, URL: c.License.URL, Identifier: c.License.Identifier}
	}
	return httpx.Info{
		Title: c.Title, Description: c.Description, Version: c.Version,
		TermsOfService: c.TermsOfService, Servers: c.Servers,
		Contact: contact, License: license,
	}
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".openapi-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("setting temporary file permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temporary spec: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replacing spec: %w", err)
	}
	return nil
}
