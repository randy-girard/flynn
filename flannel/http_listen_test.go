package main

import (
	"os"
	"strings"
	"testing"
)

func TestHTTPServerBindsBridgeIPNotNetworkAddress(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if strings.Contains(body, "Lease().Network.IP.String()") {
		t.Fatal("HTTP must not listen on the CIDR network address (100.100.9.0); that IP is never assigned and bind fails with EADDRNOTAVAIL under FLANNEL_BACKEND=alloc")
	}
	if !strings.Contains(body, "FirstUsable()") {
		t.Fatal("HTTP overlay listen and neighbor ping must use the first usable address (bridge IP)")
	}
	if !strings.Contains(body, "publicListener") {
		t.Fatal("HTTP must still listen on the host EXTERNAL_IP")
	}
}
