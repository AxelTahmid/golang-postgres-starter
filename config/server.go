package config

import (
	"crypto/tls"
	"log"
	"net/netip"
	"time"

	"github.com/AxelTahmid/tinker/internal/clientip"
)

type Server struct {
	AppEnv         string        `default:"development"`
	Name           string        `default:"tinker"`
	Host           string        `default:"0.0.0.0"`
	Domain         string        `                      required:"true"`
	Port           int           `default:"3000"`
	Logging        bool          `default:"true"`
	IdleTimeout    time.Duration `default:"60s"                         split_words:"true"`
	ReadTimeout    time.Duration `default:"30s"                         split_words:"true"`
	WriteTimeout   time.Duration `default:"30s"                         split_words:"true"`
	TrustedProxies []string      `default:"127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7" split_words:"true"`
	TLSCertPath    string        `                      required:"true" split_words:"true"`
	TLSKeyPath     string        `                      required:"true" split_words:"true"`
}

func (s Server) TrustedProxyPrefixes() []netip.Prefix {
	return clientip.ParsePrefixes(s.TrustedProxies)
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
