package accesstoken

import "testing"

func TestUpdateGitreceiveAddsKeys(t *testing.T) {
	env := map[string]string{"CONTROLLER_KEY": "k"}
	updated, err := Update("gitreceive", env)
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Fatal("expected env update")
	}
	if env["ACCESS_TOKEN_KEY"] == "" || env["ACCESS_TOKEN_SIGNING_KEY"] == "" {
		t.Fatalf("expected keypair in env, got %#v", env)
	}
}

func TestUpdateControllerAddsPublicKey(t *testing.T) {
	env := map[string]string{}
	if _, err := Update("gitreceive", map[string]string{}); err != nil {
		t.Fatal(err)
	}
	updated, err := Update("controller", env)
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Fatal("expected env update")
	}
	if env["ACCESS_TOKEN_KEY"] == "" {
		t.Fatal("expected public key")
	}
}

func TestUpdateRepairsMismatchedGitreceiveKeypair(t *testing.T) {
	pair.ok = false
	env := map[string]string{
		"ACCESS_TOKEN_KEY":         "not-a-valid-public-key",
		"ACCESS_TOKEN_SIGNING_KEY": "not-a-valid-private-key",
	}
	updated, err := Update("gitreceive", env)
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Fatal("expected mismatched keypair to be repaired")
	}
	if env["ACCESS_TOKEN_KEY"] == "" || env["ACCESS_TOKEN_SIGNING_KEY"] == "" {
		t.Fatal("expected repaired keypair")
	}
}

func TestUpdateNoChangeWhenConfigured(t *testing.T) {
	pair.ok = false
	if _, err := Update("gitreceive", map[string]string{}); err != nil {
		t.Fatal(err)
	}
	pub := pair.public
	priv := pair.private
	env := map[string]string{
		"ACCESS_TOKEN_KEY":         pub,
		"ACCESS_TOKEN_SIGNING_KEY": priv,
	}
	if updated, err := Update("gitreceive", env); err != nil || updated {
		t.Fatalf("gitreceive: updated=%v err=%v", updated, err)
	}
	if updated, err := Update("controller", map[string]string{"ACCESS_TOKEN_KEY": pub}); err != nil || updated {
		t.Fatalf("controller: updated=%v err=%v", updated, err)
	}
}

func TestUpdateControllerSyncsToGitreceivePair(t *testing.T) {
	pair.ok = false
	if _, err := Update("gitreceive", map[string]string{}); err != nil {
		t.Fatal(err)
	}
	wantPub := pair.public
	env := map[string]string{"ACCESS_TOKEN_KEY": "stale-public-key"}
	updated, err := Update("controller", env)
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Fatal("expected controller public key to be synced")
	}
	if env["ACCESS_TOKEN_KEY"] != wantPub {
		t.Fatalf("controller key = %q, want %q", env["ACCESS_TOKEN_KEY"], wantPub)
	}
}
