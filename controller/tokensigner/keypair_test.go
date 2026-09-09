package tokensigner

import "testing"

func TestKeyPairValid(t *testing.T) {
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if !KeyPairValid(pub, priv) {
		t.Fatal("generated keypair should be valid")
	}
	if KeyPairValid(pub, priv+"x") {
		t.Fatal("mutated private key should be invalid")
	}
	if KeyPairValid(pub+"x", priv) {
		t.Fatal("mutated public key should be invalid")
	}
}

func TestKeyPairValidRejectsMismatchedKeys(t *testing.T) {
	pub1, _, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	_, priv2, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if KeyPairValid(pub1, priv2) {
		t.Fatal("keys from different pairs should not validate")
	}
}
