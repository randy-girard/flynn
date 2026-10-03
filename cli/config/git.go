package config

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/kardianos/osext"
)

func CACertPath(name string) string {
	return filepath.Join(Dir(), "ca-certs", name+".pem")
}

func CACertFile(name string) (*os.File, error) {
	path := CACertPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	return os.Create(path)
}

func gitConfig(args ...string) error {
	args = append([]string{"config", "--global"}, args...)
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("error %q running %q: %q", err, strings.Join(cmd.Args, " "), out)
	}
	return nil
}

func WriteGlobalGitConfig(gitURL, caFile string) error {
	if caFile != "" {
		if err := gitConfig(fmt.Sprintf("http.%s.sslCAInfo", gitURL), caFile); err != nil {
			return err
		}
	}

	self, err := gitCredentialHelperPath()
	if err != nil {
		return err
	}

	if err := gitConfig(fmt.Sprintf("credential.%s.helper", gitURL), self+" git-credentials"); err != nil {
		return err
	}
	return nil
}

// gitCredentialHelperPath is a Flynn CLI git can exec on this machine.
// Vagrant image builds replace build-dev/bin/flynn with a Linux symlink
// (flynn-linux-*), so cluster:add must not record that path on macOS.
func gitCredentialHelperPath() (string, error) {
	self, err := osext.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(self)
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		dir = filepath.Dir(resolved)
		self = resolved
	}
	nativeName := "flynn-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		nativeName += ".exe"
	}
	candidates := []string{
		self,
		filepath.Join(dir, nativeName),
		"/usr/local/bin/flynn",
	}
	if p, err := exec.LookPath("flynn"); err == nil {
		candidates = append(candidates, p)
	}
	seen := map[string]bool{}
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		if !nativeCLI(c) {
			continue
		}
		return filepath.ToSlash(c), nil
	}
	return "", fmt.Errorf("no native flynn CLI for git-credentials (build-dev/bin/flynn is often a Linux cluster binary after vagrant builds; install with make vagrant-cli and use /usr/local/bin/flynn)")
}

func nativeCLI(path string) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	fi, err := os.Stat(resolved)
	if err != nil || fi.IsDir() {
		return false
	}
	f, err := os.Open(resolved)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return false
	}
	switch runtime.GOOS {
	case "darwin":
		return isMachO(magic)
	case "linux":
		return magic[0] == 0x7f && magic[1] == 'E' && magic[2] == 'L' && magic[3] == 'F'
	case "windows":
		return magic[0] == 'M' && magic[1] == 'Z'
	default:
		return true
	}
}

func isMachO(magic [4]byte) bool {
	switch {
	case magic[0] == 0xcf && magic[1] == 0xfa && magic[2] == 0xed && magic[3] == 0xfe:
		return true
	case magic[0] == 0xce && magic[1] == 0xfa && magic[2] == 0xed && magic[3] == 0xfe:
		return true
	case magic[0] == 0xfe && magic[1] == 0xed && magic[2] == 0xfa && magic[3] == 0xcf:
		return true
	case magic[0] == 0xca && magic[1] == 0xfe && magic[2] == 0xba && magic[3] == 0xbe:
		return true
	default:
		return false
	}
}

func RemoveGlobalGitConfig(gitURL string) {
	for _, k := range []string{
		fmt.Sprintf("http.%s", gitURL),
		fmt.Sprintf("credential.%s", gitURL),
	} {
		gitConfig("--remove-section", k)
	}
}

// ClearSystemCredentials clears any cached credentials for the given gitURL
// from the OS-specific system credential store (e.g. macOS Keychain, Windows
// Credential Manager). This prevents stale credentials from a previous cluster
// setup from causing the first git push to fail.
//
// If the credential erase command fails, a warning is printed to stderr but the
// error is not propagated so that the calling operation (cluster add/remove)
// can still succeed.
func ClearSystemCredentials(gitURL string) {
	u, err := url.Parse(gitURL)
	if err != nil {
		return
	}
	host := u.Host
	if host == "" {
		return
	}

	credInput := fmt.Sprintf("protocol=https\nhost=%s\n\n", host)

	switch runtime.GOOS {
	case "darwin":
		eraseCredential(credInput, "git", []string{"credential-osxkeychain", "erase"},
			fmt.Sprintf("Warning: Could not clear cached git credentials from macOS Keychain.\n"+
				"If your first git push fails with \"Authentication required\", run:\n"+
				"  printf \"protocol=https\\nhost=%s\\n\" | git credential-osxkeychain erase\n", host))
	case "windows":
		// Try git-credential-manager first (Git Credential Manager), fall back to wincred
		if err := eraseCredentialErr(credInput, "git", "credential-manager", "erase"); err != nil {
			eraseCredential(credInput, "git", []string{"credential-wincred", "erase"},
				fmt.Sprintf("Warning: Could not clear cached git credentials from Windows Credential Manager.\n"+
					"If your first git push fails with \"Authentication required\", run:\n"+
					"  printf \"protocol=https\\nhost=%s\\n\" | git credential-wincred erase\n", host))
		}
	case "linux":
		// Linux typically uses credential-cache (in-memory, not persistent) or
		// credential-libsecret. Try both; failures are silently ignored since
		// most Linux systems don't have a persistent system credential store.
		eraseCredentialErr(credInput, "git", "credential-cache", "erase")
		eraseCredentialErr(credInput, "git", "credential-libsecret", "erase")
	}
}

func eraseCredentialErr(input string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(input)
	return cmd.Run()
}

func eraseCredential(input string, name string, args []string, warning string) {
	if err := eraseCredentialErr(input, name, args...); err != nil {
		fmt.Fprint(os.Stderr, warning)
	}
}
