package config

import (
	"log/slog"

	"github.com/go-chi/cors"
)

type Cors struct {
	AllowedOrigins   []string `split_words:"true" default:"*"`
	AllowedMethods   []string `split_words:"true" default:"GET,POST,PUT,DELETE,PATCH,OPTIONS"`
	AllowedHeaders   []string `split_words:"true" default:"*"`
	ExposedHeaders   []string `split_words:"true" default:"*"`
	AllowCredentials bool     `split_words:"true" default:"true"`
	MaxAge           int      `split_words:"true" default:"300"`
}

// ToCorsOptions maps Cors struct to cors.Options
func (c Cors) CorsOptions() cors.Options {
	slog.Info("Allowed CORS Origins ==>", "HostList", c.AllowedOrigins)

	// AllowedHeaders   []string `split_words:"true" default:"Origin,Authorization,User-Agent,Content-Type,Accept,Accept-Encoding,Accept-Language,Cache-Control,Connection,DNT,Host,Origin,Pragma,Referer"`
	// ExposedHeaders   []string `split_words:"true" default:"Content-Length,Content-Type,Date,ETag,Expires,Last-Modified,Pragma"`
	return cors.Options{
		AllowedOrigins:   c.AllowedOrigins,
		AllowedMethods:   c.AllowedMethods,
		AllowedHeaders:   c.AllowedHeaders,
		ExposedHeaders:   c.ExposedHeaders,
		AllowCredentials: c.AllowCredentials,
		MaxAge:           c.MaxAge,
	}
}
