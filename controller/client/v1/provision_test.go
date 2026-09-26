package v1controller

import (
	"os"
	"strings"
	"testing"
)

func TestProvisionResourceKeepsCallerDialer(t *testing.T) {
	b, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "httphelper.ClientForProvision(c.HTTP)") {
		t.Fatal("provision must keep the caller's dialer so flynn-host can resolve *.discoverd")
	}
	if strings.Contains(src, "httphelper.ProvisionClient)") {
		t.Fatal("ProvisionClient uses system DNS and breaks plugin install on the host")
	}
}
