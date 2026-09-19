//go:build !unix

package cli

import (
	"fmt"
	"os"
)

func fileIsTerminal(f *os.File) bool {
	return false
}

func readHiddenFromTerminal(f *os.File) (string, error) {
	return "", fmt.Errorf("hidden input is not supported on this platform")
}
