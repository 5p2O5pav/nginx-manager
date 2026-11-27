package config

import "testing"

func TestRouteDefaultsAndValidation(t *testing.T) {
	r, err := NormalizeRoute(Route{Target: "127.0.0.1:30001"})
	if err != nil || r.Path != "/" || r.Target != "http://127.0.0.1:30001" {
		t.Fatalf("%+v %v", r, err)
	}
	for _, target := range []string{"http://a.example/path", "http://a.example/?x=1", "http://a.example:99999", "http://x;return 200"} {
		if _, err := NormalizeRoute(Route{Target: target}); err == nil {
			t.Errorf("accepted %q", target)
		}
	}
	for _, path := range []string{"/../secret", "/api;return", "/a//b", "/$host"} {
		if _, err := NormalizeRoute(Route{Path: path, Target: "localhost:80"}); err == nil {
			t.Errorf("accepted path %q", path)
		}
	}
}

func TestDuplicateDomainAndPath(t *testing.T) {
	c := Config{Version: 1, Domains: []Domain{{Name: "Example.com"}, {Name: "example.com"}}}
	if c.Normalize() == nil {
		t.Fatal("duplicate domain accepted")
	}
	c = Config{Version: 1, Domains: []Domain{{Name: "example.com", Routes: []Route{{Target: "localhost"}, {Path: "/", Target: "localhost"}}}}}
	if c.Normalize() == nil {
		t.Fatal("duplicate path accepted")
	}
}

func TestUpstreamTLSNameNormalized(t *testing.T) {
	r, err := NormalizeRoute(Route{Target: "https://127.0.0.1:443", UpstreamTLS: &UpstreamTLS{ServerName: " EXAMPLE.COM\n"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.UpstreamTLS.ServerName != "example.com" {
		t.Fatalf("unexpected TLS name %q", r.UpstreamTLS.ServerName)
	}
}
