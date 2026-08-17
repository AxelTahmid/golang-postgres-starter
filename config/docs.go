package config

// DocsConfig is the canonical, zero-environment configuration used to
// generate the committed OpenAPI document deterministically.
func DocsConfig() *Config {
	return &Config{OpenAPI: OpenAPI{
		Enabled:     true,
		Title:       "Tinker API",
		Description: "API documentation for the Go PostgreSQL starter",
		Version:     "1.0.0",
		Servers:     []string{"https://localhost:3000"},
		License: &License{
			Name: "MIT",
			URL:  "https://opensource.org/license/mit",
		},
	}}
}
