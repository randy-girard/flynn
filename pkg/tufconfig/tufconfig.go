package tufconfig

import (
	"encoding/json"

	"github.com/flynn/go-tuf/data"
)

var (
	// these constants are overridden at build time (see builder/go-wrapper.sh)
	RootKeysJSON = `[{"keytype":"ed25519","keyval":{"public":"9e86ad840b99a03001220059648ff59b7ffe113de86438ae96dfccd190c66466"}}]`
	Repository   = "https://dl.flynn.cloud.randygirard.com/tuf"
)

var RootKeys []*data.Key

func init() {
	if err := json.Unmarshal([]byte(RootKeysJSON), &RootKeys); err != nil {
		panic("error decoding root keys")
	}
}
