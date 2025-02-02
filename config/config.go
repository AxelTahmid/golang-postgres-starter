package config

import "sync"

type Config struct {
	Server
	Cors
	Secure
	Database
	Jwt
}

var (
	once sync.Once
	conf *Config
)

func NewConfig() *Config {
	once.Do(func() {
		conf = &Config{
			Server:   serverConfig(),
			Cors:     corsConfig(),
			Secure:   secureConfig(),
			Database: dbConfig(),
			Jwt:      jwtConfig(),
		}
	},
	)
	return conf
}
