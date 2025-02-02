package config

import (
	"time"

	"github.com/kelseyhightower/envconfig"
)

type Jwt struct {
	JwtPubKeyPath      string        `split_words:"true" required:"true"`
	JwtPvtKeyPath      string        `split_words:"true" required:"true"`
	AccessTokenIssuer  string        `split_words:"true" default:"auth-access"`
	RefreshTokenIssuer string        `split_words:"true" default:"auth-refresh"`
	AccessExpiryTime   time.Duration `split_words:"true" default:"10m"`
	RefreshExpiryTime  time.Duration `split_words:"true" default:"72h"`
}

func jwtConfig() Jwt {
	var j Jwt
	envconfig.MustProcess("", &j)
	return j
}
