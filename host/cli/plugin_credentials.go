package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/pkg/plugin"
)

const credentialTokenMissing = "no GitHub token provided; pass --token-file, pipe a token on stdin, or run on a TTY to paste interactively"

type credentialIO struct {
	credsPath  string
	stdin      *os.File
	stdout     io.Writer
	stderr     io.Writer
	isTerminal func(*os.File) bool
	readHidden func(*os.File) (string, error)
}

func defaultCredentialIO() credentialIO {
	return credentialIO{
		stdin:  os.Stdin,
		stdout: os.Stdout,
		stderr: os.Stderr,
	}
}

func githubCredentialsHost(args *docopt.Args) (string, error) {
	host := strings.TrimSpace(args.String["<host>"])
	if host == "" {
		return "", fmt.Errorf("host is required (github or github.com; github.com is the default GitHub host)")
	}
	return plugin.NormalizeGitHubHost(host), nil
}

func runPluginCredentialsSet(args *docopt.Args) error {
	return setPluginGitHubCredentials(args, defaultCredentialIO())
}

func runPluginCredentialsUnset(args *docopt.Args) error {
	return unsetPluginGitHubCredentials(args, defaultCredentialIO())
}

func runPluginCredentialsShow(args *docopt.Args) error {
	return showPluginGitHubCredentials(args, defaultCredentialIO())
}

func setPluginGitHubCredentials(args *docopt.Args, in credentialIO) error {
	host, err := githubCredentialsHost(args)
	if err != nil {
		return err
	}
	token, err := readCredentialTokenFrom(args.String["--token-file"], in)
	if err != nil {
		return err
	}
	if err := plugin.SetGitHubCredentials(in.credsPath, host, token, args.String["--api"]); err != nil {
		return err
	}
	fmt.Fprintf(in.stdout, "%s credentials set\n", host)
	return nil
}

func unsetPluginGitHubCredentials(args *docopt.Args, in credentialIO) error {
	host, err := githubCredentialsHost(args)
	if err != nil {
		return err
	}
	removed, err := plugin.UnsetGitHubCredentials(in.credsPath, host)
	if err != nil {
		return err
	}
	if !removed {
		fmt.Fprintf(in.stdout, "%s credentials: nothing stored\n", host)
		return nil
	}
	fmt.Fprintf(in.stdout, "%s credentials: removed\n", host)
	return nil
}

func showPluginGitHubCredentials(args *docopt.Args, in credentialIO) error {
	host, err := githubCredentialsHost(args)
	if err != nil {
		return err
	}
	set, api, err := plugin.CredentialStatus(in.credsPath, host)
	if err != nil {
		return err
	}
	if !set {
		fmt.Fprintf(in.stdout, "%s credentials: unset\n", host)
		return nil
	}
	fmt.Fprintf(in.stdout, "%s credentials: set\n", host)
	if api != "" {
		fmt.Fprintf(in.stdout, "api: %s\n", api)
	}
	return nil
}

func readCredentialToken(path string) (string, error) {
	return readCredentialTokenFrom(path, defaultCredentialIO())
}

func readCredentialTokenFrom(path string, in credentialIO) (string, error) {
	if strings.TrimSpace(path) != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		tok := strings.TrimSpace(string(data))
		if tok == "" {
			return "", fmt.Errorf("token file is empty")
		}
		return tok, nil
	}

	stdin := in.stdin
	if stdinHasStream(stdin) {
		tok, err := plugin.ReadToken(stdin)
		if err != nil {
			return "", err
		}
		if tok == "" {
			return "", fmt.Errorf("%s", credentialTokenMissing)
		}
		return tok, nil
	}

	if isCredentialTerminal(stdin, in.isTerminal) {
		return promptPasteToken(stdin, in)
	}

	return "", fmt.Errorf("%s", credentialTokenMissing)
}

func stdinHasStream(f *os.File) bool {
	if f == nil {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice == 0
}

func isCredentialTerminal(f *os.File, override func(*os.File) bool) bool {
	if override != nil {
		return override(f)
	}
	return fileIsTerminal(f)
}

func promptPasteToken(stdin *os.File, in credentialIO) (string, error) {
	fmt.Fprint(in.stderr, "Paste a GitHub token (input is hidden): ")
	readHidden := in.readHidden
	if readHidden == nil {
		readHidden = readHiddenFromTerminal
	}
	tok, err := readHidden(stdin)
	fmt.Fprintln(in.stderr)
	if err != nil {
		fmt.Fprintln(in.stderr, "could not hide input; type the token and press Enter")
		tok, err = readLineToken(stdin)
		if err != nil {
			return "", err
		}
	}
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return "", fmt.Errorf("%s", credentialTokenMissing)
	}
	return tok, nil
}

func readLineToken(r io.Reader) (string, error) {
	if r == nil {
		return "", fmt.Errorf("%s", credentialTokenMissing)
	}
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
