//go:build tools
// +build tools

// This file declares dependencies on tool binaries that are used during the build.
// This ensures they are tracked in go.mod and can be vendored.

package tools

import (
	_ "github.com/golang/protobuf/protoc-gen-go"
)

