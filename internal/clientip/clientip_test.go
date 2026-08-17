package clientip_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AxelTahmid/tinker/internal/clientip"
)

var trusted = clientip.ParsePrefixes([]string{
	"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7",
})

func request(remote string, forwarded string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	if forwarded != "" {
		r.Header.Set("X-Forwarded-For", forwarded)
	}
	return r
}

func TestResolve(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, remote, forwarded, want string
	}{
		{"direct ignores spoofed header", "203.0.113.7:1234", "198.51.100.9", "203.0.113.7"},
		{"trusted proxy", "127.0.0.1:1234", "203.0.113.7", "203.0.113.7"},
		{"rightmost untrusted hop", "127.0.0.1:1234", "198.51.100.9, 203.0.113.7", "203.0.113.7"},
		{"mapped IPv4", "127.0.0.1:1234", "::ffff:203.0.113.7", "203.0.113.7"},
	}
	resolver := clientip.NewResolver(trusted)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addr, ok := resolver.Resolve(request(tc.remote, tc.forwarded))
			if !ok || addr.String() != tc.want {
				t.Fatalf("Resolve() = %q, %v; want %q, true", addr, ok, tc.want)
			}
		})
	}
}

func TestMiddlewarePublishesAddressAndStripsTrueClientIP(t *testing.T) {
	t.Parallel()
	resolver := clientip.NewResolver(trusted)
	var got, header string
	handler := resolver.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = clientip.FromRequest(r)
		header = r.Header.Get("True-Client-IP")
	}))
	r := request("127.0.0.1:1234", "203.0.113.7")
	r.Header.Set("True-Client-IP", "198.51.100.9")
	handler.ServeHTTP(httptest.NewRecorder(), r)
	if got != "203.0.113.7" || header != "" {
		t.Fatalf("address = %q, True-Client-IP = %q", got, header)
	}
}

func TestMalformedChainDoesNotTrustEntriesToItsLeft(t *testing.T) {
	t.Parallel()
	resolver := clientip.NewResolver(trusted)
	addr, ok := resolver.Resolve(request("127.0.0.1:1234", "203.0.113.7, garbage"))
	if !ok || addr.String() != "127.0.0.1" {
		t.Fatalf("Resolve() = %q, %v; want peer", addr, ok)
	}
}
