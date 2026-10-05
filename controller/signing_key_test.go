package main

import "testing"

func TestAppReleaseSigningKeyNilDB(t *testing.T) {
	if got := appReleaseSigningKey(nil, "gitreceive"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestTokenSignerFromEnvAndDBEmpty(t *testing.T) {
	t.Setenv("ACCESS_TOKEN_SIGNING_KEY", "")
	t.Setenv("ACCESS_TOKEN_PRIVATE_KEY", "")
	s, err := tokenSignerFromEnvAndDB(nil)
	if err != nil {
		t.Fatal(err)
	}
	if s != nil {
		t.Fatal("expected nil signer")
	}
}
