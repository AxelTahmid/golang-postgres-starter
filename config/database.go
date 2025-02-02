package config

import (
	"time"

	"github.com/kelseyhightower/envconfig"
)

type Database struct {
	ConnectTimeout    time.Duration `split_words:"true" default:"15s"`
	MaxConnLifetime   time.Duration `split_words:"true" default:"30m"`
	MaxConnIdleTime   time.Duration `split_words:"true" default:"1m"`
	HealthCheckPeriod time.Duration `split_words:"true" default:"60s"`
	PoolMax           int32         `split_words:"true" default:"50"`
	PoolMin           int32         `split_words:"true" default:"0"`
	Url               string        `required:"true"`
	SslMode           string        `default:"disable"`
	TimeZone          string        `split_words:"true" default:"America/Regina"`
}

func dbConfig() Database {
	var d Database
	envconfig.MustProcess("DB", &d)

	return d
}
