package config

import (
	"log"
	"sync"

	"github.com/kelseyhightower/envconfig"
)

// The Field name will enforce prefix for env key, i.e. SERVER_
type Config struct {
	Server  Server
	Cors    Cors
	Secure  Secure
	DB      Database
	Jwt     Jwt
	OpenAPI OpenAPI
}

var (
	confOnce     sync.Once
	confInstance *Config
)

// InitConfig initializes and returns the application configuration.
// It ensures the configuration is loaded only confOnce.
func InitConfig() (*Config, error) {
	log.Println("Parsing env config")
	var err error
	confOnce.Do(func() {
		confInstance = &Config{}
		err = envconfig.Process("", confInstance)
	})
	log.Println("Env config parsed")
	return confInstance, err
}
