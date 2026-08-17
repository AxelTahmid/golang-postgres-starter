package config

type OpenAPI struct {
	Enabled        bool     `split_words:"true" default:"true"`
	Title          string   `split_words:"true" default:"Tinker API"`
	Description    string   `split_words:"true" default:"API documentation for the Go PostgreSQL starter"`
	Version        string   `split_words:"true" default:"1.0.0"`
	Servers        []string `split_words:"true" default:"https://localhost:3000"`
	TermsOfService string   `split_words:"true"`
	Contact        *Contact `split_words:"true"`
	License        *License `split_words:"true"`
}

type Contact struct {
	Name  string `split_words:"true"`
	URL   string `split_words:"true"`
	Email string `split_words:"true"`
}

type License struct {
	Name       string `split_words:"true" default:"MIT"`
	URL        string `split_words:"true" default:"https://opensource.org/license/mit"`
	Identifier string `split_words:"true"`
}
