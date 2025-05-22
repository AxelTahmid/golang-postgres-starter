package config

import (
	"time"
)

type Jwt struct {
	PubKeyPath         string        `split_words:"true" required:"true"`
	PvtKeyPath         string        `split_words:"true" required:"true"`
	AccessTokenIssuer  string        `split_words:"true"                 default:"auth-access"`
	RefreshTokenIssuer string        `split_words:"true"                 default:"auth-refresh"`
	AccessExpiryTime   time.Duration `split_words:"true"                 default:"5m"`
	RefreshExpiryTime  time.Duration `split_words:"true"                 default:"30m"`
	ClockSkew          time.Duration `split_words:"true"                 default:"10s"`
}
