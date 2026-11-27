package nginx

import (
	"strings"
	"testing"
	"time"

	"nginx-manager/internal/config"
)

func TestUnissuedDomainExposesChallengeOnly(t *testing.T) {
	c := config.Config{Version: 1, Domains: []config.Domain{{Name: "example.com", Enabled: true, Routes: []config.Route{{Target: "localhost:30001"}}}}}
	out, err := Render(c, Paths{SSLDir: "/tmp/ssl", Webroot: "/tmp/acme", DefaultDir: "/tmp/default"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "location ^~ /.well-known/acme-challenge/") || !strings.Contains(out, "location / { return 444; }") {
		t.Fatal(out)
	}
	if strings.Contains(out, "proxy_pass") || strings.Contains(out, "ssl_certificate ") {
		t.Fatal("uncertified domain exposed")
	}
}
