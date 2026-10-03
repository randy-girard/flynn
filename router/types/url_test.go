package router

import (
	"strings"
	"testing"
)

func TestPublicURLs(t *testing.T) {
	cluster := "1.localflynn.com"
	got := (&Route{Type: "http", Domain: "app-one.1.localflynn.com"}).PublicURLs(cluster)
	want := "https://app-one.1.localflynn.com,http://app-one.1.localflynn.com"
	if strings.Join(got, ",") != want {
		t.Fatalf("included = %v", got)
	}

	custom := (&Route{Type: "http", Domain: "www.example.com"}).PublicURLs(cluster)
	if strings.Join(custom, ",") != "http://www.example.com" {
		t.Fatalf("custom without cert = %v", custom)
	}

	tls := (&Route{Type: "http", Domain: "www.example.com", LegacyTLSCert: "CERT"}).PublicURLs(cluster)
	if tls[0] != "https://www.example.com" {
		t.Fatalf("custom with cert = %v", tls)
	}

	path := (&Route{Type: "http", Domain: "app-one.1.localflynn.com", Path: "/api"}).PublicURLs(cluster)
	if path[0] != "https://app-one.1.localflynn.com/api" {
		t.Fatalf("path = %v", path)
	}

	tcp := (&Route{Type: "tcp", Domain: "redis-lagoon-59415.1.localflynn.com", Port: 3000}).PublicURLs(cluster)
	if strings.Join(tcp, ",") != "tcp://redis-lagoon-59415.1.localflynn.com:3000" {
		t.Fatalf("tcp = %v", tcp)
	}

	if (&Route{Type: "http"}).PublicURLs(cluster) != nil {
		t.Fatal("empty domain")
	}
}
