package accesstoken

import (
	"github.com/flynn/flynn/controller/tokensigner"
)

// pair holds the cluster access-token keypair for the duration of one updater
// run so controller, tarreceive, and gitreceive releases share the same keys.
var pair struct {
	public, private string
	ok              bool
}

func seedPair(publicKey, privateKey string) {
	pair.public, pair.private, pair.ok = publicKey, privateKey, true
}

func generatedPair() (publicKey, privateKey string, err error) {
	if pair.ok {
		return pair.public, pair.private, nil
	}
	publicKey, privateKey, err = tokensigner.GenerateKeyPair()
	if err != nil {
		return "", "", err
	}
	seedPair(publicKey, privateKey)
	return publicKey, privateKey, nil
}

// Update adds or repairs access-token env vars on a system app release when
// needed, so clusters bootstrapped before gen-access-token-key can migrate on
// update without a full re-bootstrap. Returns true if env was changed.
//
// gitreceive is updated before controller/tarreceive in the updater deploy
// order, so a repaired keypair on gitreceive is propagated to verifiers via
// the cached pair.
func Update(appName string, env map[string]string) (bool, error) {
	if env == nil {
		return false, nil
	}
	switch appName {
	case "gitreceive":
		return updateGitreceive(env)
	case "controller", "tarreceive":
		return updateVerifier(env)
	default:
		return false, nil
	}
}

func updateGitreceive(env map[string]string) (bool, error) {
	pub := env["ACCESS_TOKEN_KEY"]
	priv := env["ACCESS_TOKEN_SIGNING_KEY"]
	if priv != "" && tokensigner.KeyPairValid(pub, priv) {
		seedPair(pub, priv)
		return false, nil
	}
	publicKey, privateKey, err := generatedPair()
	if err != nil {
		return false, err
	}
	changed := env["ACCESS_TOKEN_KEY"] != publicKey || env["ACCESS_TOKEN_SIGNING_KEY"] != privateKey
	env["ACCESS_TOKEN_KEY"] = publicKey
	env["ACCESS_TOKEN_SIGNING_KEY"] = privateKey
	return changed, nil
}

func updateVerifier(env map[string]string) (bool, error) {
	if pair.ok {
		if env["ACCESS_TOKEN_KEY"] == pair.public {
			return false, nil
		}
		env["ACCESS_TOKEN_KEY"] = pair.public
		return true, nil
	}
	if env["ACCESS_TOKEN_KEY"] != "" {
		return false, nil
	}
	publicKey, _, err := generatedPair()
	if err != nil {
		return false, err
	}
	env["ACCESS_TOKEN_KEY"] = publicKey
	return true, nil
}
