package cli

import "testing"

func TestTokenRotateRegistered(t *testing.T) {
	if commands["token:rotate"] == nil {
		t.Fatal("token:rotate must be a flynn-host command")
	}
}

func TestRotateServiceEnvDropsClusterKey(t *testing.T) {
	env := map[string]string{
		"CONTROLLER_KEY":           "cluster-god-key",
		"ACCESS_TOKEN_SIGNING_KEY": "sign",
		"DATABASE_URL":             "postgres://app",
	}
	rotateServiceEnv(env, "QUOTA_TOKEN", "quota-token-value", "account:quota", "cluster-god-key")
	if _, ok := env["CONTROLLER_KEY"]; ok {
		t.Fatal("cluster key was copied onto the app")
	}
	if _, ok := env["ACCESS_TOKEN_SIGNING_KEY"]; ok {
		t.Fatal("signing key was copied onto the app")
	}
	if env["QUOTA_TOKEN"] != "quota-token-value" {
		t.Fatalf("token=%q", env["QUOTA_TOKEN"])
	}
	if env["BILLING_AUTH_HASH"] == "" || env["BILLING_AUTH_HASH"] == "cluster-god-key" {
		t.Fatalf("hash=%q", env["BILLING_AUTH_HASH"])
	}
	if env["DATABASE_URL"] != "postgres://app" {
		t.Fatalf("env=%v", env)
	}
}
