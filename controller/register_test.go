package main

import (
	"testing"
)

func TestControllerHTTPInstanceOmitsAuthKey(t *testing.T) {
	inst := controllerHTTPInstance(":80")
	if inst.Addr != ":80" || inst.Proto != "http" {
		t.Fatalf("%+v", inst)
	}
	if inst.Meta != nil {
		if _, ok := inst.Meta["AUTH_KEY"]; ok {
			t.Fatal("SEC-028: AUTH_KEY must not be published in discoverd instance meta")
		}
	}
}
