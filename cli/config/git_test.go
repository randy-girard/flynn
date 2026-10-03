package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNativeCLIRejectsWrongOSMagic(t *testing.T) {
	dir := t.TempDir()
	elf := filepath.Join(dir, "elf")
	if err := os.WriteFile(elf, []byte{0x7f, 'E', 'L', 'F'}, 0755); err != nil {
		t.Fatal(err)
	}
	macho := filepath.Join(dir, "macho")
	if err := os.WriteFile(macho, []byte{0xcf, 0xfa, 0xed, 0xfe}, 0755); err != nil {
		t.Fatal(err)
	}
	switch runtime.GOOS {
	case "darwin":
		if nativeCLI(elf) {
			t.Fatal("linux ELF must not be a macOS git credential helper")
		}
		if !nativeCLI(macho) {
			t.Fatal("Mach-O flynn must be accepted on darwin")
		}
	case "linux":
		if !nativeCLI(elf) {
			t.Fatal("ELF flynn must be accepted on linux")
		}
		if nativeCLI(macho) {
			t.Fatal("Mach-O must not be a linux git credential helper")
		}
	}
}

func TestGitCredentialHelperPrefersSiblingNativeCLI(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("native magic checks are OS-specific")
	}
	dir := t.TempDir()
	linux := filepath.Join(dir, "flynn-linux-arm64")
	if err := os.WriteFile(linux, []byte{0x7f, 'E', 'L', 'F'}, 0755); err != nil {
		t.Fatal(err)
	}
	nativeName := "flynn-" + runtime.GOOS + "-" + runtime.GOARCH
	native := filepath.Join(dir, nativeName)
	var magic []byte
	if runtime.GOOS == "darwin" {
		magic = []byte{0xcf, 0xfa, 0xed, 0xfe}
	} else {
		magic = []byte{0x7f, 'E', 'L', 'F'}
	}
	if err := os.WriteFile(native, magic, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "flynn")
	if err := os.Symlink(linux, link); err != nil {
		t.Fatal(err)
	}
	if nativeCLI(link) && runtime.GOOS == "darwin" {
		t.Fatal("symlink to linux ELF must be rejected on darwin")
	}
	if !nativeCLI(native) {
		t.Fatal("sibling native CLI must be accepted")
	}
}
