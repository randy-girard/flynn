//go:build unix

package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func fileIsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	_, err := unix.IoctlGetTermios(int(f.Fd()), uint(ioctlReadTermios))
	return err == nil
}

func readHiddenFromTerminal(f *os.File) (string, error) {
	if f == nil {
		return "", fmt.Errorf("stdin is unavailable")
	}
	fd := int(f.Fd())
	old, err := unix.IoctlGetTermios(fd, uint(ioctlReadTermios))
	if err != nil {
		return "", err
	}
	raw := *old
	raw.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, uint(ioctlWriteTermios), &raw); err != nil {
		return "", err
	}
	defer unix.IoctlSetTermios(fd, uint(ioctlWriteTermios), old)

	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
