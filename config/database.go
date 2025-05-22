package config

import (
	"time"
)

type Database struct {
	ConnectTimeout    time.Duration `split_words:"true" default:"15s"`
	MaxConnLifetime   time.Duration `split_words:"true" default:"30m"`
	MaxConnIdleTime   time.Duration `split_words:"true" default:"1m"`
	HealthCheckPeriod time.Duration `split_words:"true" default:"60s"`
	PoolMax           int32         `split_words:"true" default:"50"`
	PoolMin           int32         `split_words:"true" default:"0"`
	Url               string        `                                            required:"true"`
	RootUrl           string        `split_words:"true"                          required:"true"`
	RiverUrl          string        `split_words:"true"                          required:"true"`
	SslMode           string        `                   default:"disable"`
	TimeZone          string        `split_words:"true" default:"America/Regina"`
}
