//go:build !linux

package cleanup

func unmountTree(string) {}
