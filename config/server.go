package config

import (
	"crypto/tls"
	"log"
	"time"

	"github.com/kelseyhightower/envconfig"
)

type Server struct {
	AppEnv       string        `default:"development"`
	Name         string        `default:"go-api"`
	Host         string        `default:"0.0.0.0"`
	Port         int           `default:"3000"`
	Logging      bool          `default:"true"`
	IdleTimeout  time.Duration `split_words:"true" default:"60s"`
	ReadTimeout  time.Duration `split_words:"true" default:"5s"`
	WriteTimeout time.Duration `split_words:"true" default:"10s"`
	TLSCertPath  string        `split_words:"true" required:"true"`
	TLSKeyPath   string        `split_words:"true" required:"true"`
}

func serverConfig() Server {
	var s Server

	envconfig.MustProcess("", &s)

	return s
}

func (s Server) TLSOptions() *tls.Config {
	serverTLSCert, err := tls.LoadX509KeyPair(s.TLSCertPath, s.TLSKeyPath)
	if err != nil {
		log.Fatalf("Error loading certificate and key file: %v", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{serverTLSCert},
	}
}
