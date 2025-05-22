package config

import (
	"github.com/unrolled/secure"
)

type Secure struct {
	AllowedHosts          []string          `split_words:"true" default:"localhost"`
	HostsProxyHeaders     []string          `split_words:"true" default:"X-Forwarded-Host"`
	SSLProxyHeaders       map[string]string `split_words:"true" default:"X-Forwarded-Proto:https"`
	SSLHost               string            `split_words:"true" default:"localhost"`
	ContentSecurityPolicy string            `split_words:"true" default:"script-src $NONCE"`
	AllowedHostsAreRegex  bool              `split_words:"true" default:"false"`
	SSLRedirect           bool              `split_words:"true" default:"true"`
	STSSeconds            int64             `split_words:"true" default:"31536000"`
	STSIncludeSubdomains  bool              `split_words:"true" default:"true"`
	STSPreload            bool              `split_words:"true" default:"true"`
	FrameDeny             bool              `split_words:"true" default:"true"`
	ContentTypeNosniff    bool              `split_words:"true" default:"true"`
	BrowserXssFilter      bool              `split_words:"true" default:"true"`
}

func (s Secure) SecureOptions() *secure.Secure {
	return secure.New(secure.Options{
		STSSeconds:            s.STSSeconds,
		STSIncludeSubdomains:  s.STSIncludeSubdomains,
		STSPreload:            s.STSPreload,
		FrameDeny:             s.FrameDeny,
		ContentTypeNosniff:    s.ContentTypeNosniff,
		BrowserXssFilter:      s.BrowserXssFilter,
		ContentSecurityPolicy: s.ContentSecurityPolicy,
		// AllowedHosts:          conf.AllowedHosts,
		// AllowedHostsAreRegex:  conf.AllowedHostsAreRegex,
		// HostsProxyHeaders:     conf.HostsProxyHeaders,
		// SSLRedirect:           conf.SSLRedirect,
		// SSLHost:               conf.SSLHost,
		// SSLProxyHeaders:       conf.SSLProxyHeaders,
	})
}
