package tufconfig

import (
	"encoding/json"

	"github.com/flynn/go-tuf/data"
)

var (
	// these constants are overridden at build time (see builder/go-wrapper.sh)
	RootKeysJSON = `[{"keytype":"ed25519","keyval":{"public":"27da292214dd17fec4c957af3786bcb1267a233290610292054b0f9114a557d7"}}]`
	Repository   = "https://dl.flynn.cloud.randygirard.com/tuf"
)

var RootKeys []*data.Key

func init() {
	if err := json.Unmarshal([]byte(RootKeysJSON), &RootKeys); err != nil {
		panic("error decoding root keys")
	}
}
