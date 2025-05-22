package config

type OpenAPI struct {
	Enabled     bool     `split_words:"true" default:"false"`
	Host        string   `split_words:"true" default:"https://kleos.tahmid.org"`
	BasePath    string   `split_words:"true" default:"/"`
	Title       string   `split_words:"true" default:"Kleos"`
	Description string   `split_words:"true" default:"API docs for kleos"`
	Version     string   `split_words:"true" default:"1.0.0"`
	Contact     *Contact `split_words:"true"`
	License     *License `split_words:"true"`
	Server      string   `split_words:"true" default:"https://localhost:3000"`
}

type Contact struct {
	Name  string `split_words:"true" default:"Shahadat Hossain"`
	URL   string `split_words:"true"`
	Email string `split_words:"true" default:"support@trttech.ca"`
}

type License struct {
	Name       string `split_words:"true" default:"CLOSED"`
	URL        string `split_words:"true" default:"https://trttech.ca/terms"`
	Identifier string `split_words:"true"`
}
