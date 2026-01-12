package tufconfig

import (
	"encoding/json"

	"github.com/flynn/go-tuf/data"
)

var (
	// these constants are overridden at build time (see builder/go-wrapper.sh)
	RootKeysJSON = `[{"keytype":"ed25519","keyval":{"public":"0a6b02d6c7518c4e3f881a9e5b5f1306fe265d612d99a2cf75d487f5b3547ff4"}}]`
	Repository   = "https://dl.flynn.cloud.randygirard.com/tuf"
)

var RootKeys []*data.Key

func init() {
	if err := json.Unmarshal([]byte(RootKeysJSON), &RootKeys); err != nil {
		panic("error decoding root keys")
	}
}
