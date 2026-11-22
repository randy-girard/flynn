package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

// EDGE-02: submodule URLs come from the pushed .gitmodules, so they are
// tenant input. The pre-receive hook fetches them as root in the system
// gitreceive job, which sits on the cluster network and (for opted-in apps)
// holds the cluster SSH client key. The hook asks
// `gitreceived -check-submodule-url <url> <app ID or name>...` before each
// fetch. The check allows https to public addresses for every app, and ssh
// to public addresses only for apps named in SSH_CLIENT_KEY_APPS. It prints
// three lines that the hook uses as is:
//
//	protocol   value for GIT_ALLOW_PROTOCOL (https or ssh)
//	url        URL to fetch
//	pin        https: http.curloptResolve entry (host:port:addr,...), so git
//	           connects only to the addresses checked here
//	           ssh: the original host name for HostKeyAlias; the URL already
//	           carries the checked address

const submoduleCheckFlag = "-check-submodule-url"

type submoduleSource struct {
	Protocol string
	URL      string
	Pin      string
}

func runCheckSubmoduleURL(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gitreceived %s <url> [app]...", submoduleCheckFlag)
	}
	sshOK := submoduleSSHAllowed(os.Getenv("SSH_CLIENT_KEY_APPS"), args[1:])
	src, err := checkSubmoduleURL(args[0], sshOK, lookupSubmoduleHost)
	if err != nil {
		return fmt.Errorf("refusing submodule URL: %v", err)
	}
	fmt.Printf("%s\n%s\n%s\n", src.Protocol, src.URL, src.Pin)
	return nil
}

func lookupSubmoduleHost(host string) ([]netip.Addr, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// submoduleSSHAllowed matches the slugbuilder opt-in (BUILD-08): "*" or a
// comma-separated list of app names or IDs.
func submoduleSSHAllowed(apps string, idents []string) bool {
	for _, a := range strings.Split(apps, ",") {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		for _, id := range idents {
			if id != "" && (a == "*" || a == id) {
				return true
			}
		}
	}
	return false
}

func checkSubmoduleURL(raw string, sshOK bool, lookup func(string) ([]netip.Addr, error)) (submoduleSource, error) {
	if raw == "" {
		return submoduleSource{}, errors.New("empty URL")
	}
	if strings.HasPrefix(raw, "-") {
		return submoduleSource{}, errors.New("URL starts with '-'")
	}
	for _, r := range raw {
		if r <= ' ' || r == 0x7f {
			return submoduleSource{}, errors.New("URL contains whitespace or control characters")
		}
	}
	// "<helper>::<address>" selects a git remote helper (ext:: runs commands).
	if strings.Contains(raw, "::") {
		return submoduleSource{}, errors.New("remote helper URLs are not allowed")
	}
	if strings.Contains(raw, "://") {
		return checkSubmoduleSchemeURL(raw, sshOK, lookup)
	}
	return checkSubmoduleSCPURL(raw, sshOK, lookup)
}

func checkSubmoduleSchemeURL(raw string, sshOK bool, lookup func(string) ([]netip.Addr, error)) (submoduleSource, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return submoduleSource{}, err
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "https":
	case "ssh", "git+ssh", "ssh+git":
		if !sshOK {
			return submoduleSource{}, errors.New("ssh submodules need the app in gitreceive SSH_CLIENT_KEY_APPS")
		}
	default:
		return submoduleSource{}, fmt.Errorf("scheme %q is not allowed (use https)", u.Scheme)
	}
	if u.Opaque != "" || u.Host == "" {
		return submoduleSource{}, errors.New("URL has no host")
	}
	if u.User != nil && !validSSHUser(u.User.Username()) {
		return submoduleSource{}, errors.New("bad user name in URL")
	}
	host, addrs, err := checkSubmoduleHost(u.Hostname(), lookup)
	if err != nil {
		return submoduleSource{}, err
	}
	port := u.Port()
	if scheme == "https" {
		if port == "" {
			port = "443"
		}
		return submoduleSource{Protocol: "https", URL: raw, Pin: curlResolve(host, port, addrs)}, nil
	}
	if u.User == nil {
		return submoduleSource{}, errors.New("ssh URL needs a user")
	}
	if _, set := u.User.Password(); set {
		return submoduleSource{}, errors.New("ssh URL must not carry a password")
	}
	pinned := *u
	if port == "" {
		pinned.Host = hostForURL(addrs[0])
	} else {
		pinned.Host = net.JoinHostPort(addrs[0].String(), port)
	}
	return submoduleSource{Protocol: "ssh", URL: pinned.String(), Pin: host}, nil
}

// checkSubmoduleSCPURL handles git's "[user@]host:path" ssh shorthand. A
// string with no colon before the first slash is a local path.
func checkSubmoduleSCPURL(raw string, sshOK bool, lookup func(string) ([]netip.Addr, error)) (submoduleSource, error) {
	colon := strings.Index(raw, ":")
	slash := strings.Index(raw, "/")
	if colon < 0 || (slash >= 0 && slash < colon) {
		return submoduleSource{}, errors.New("local paths are not allowed")
	}
	if !sshOK {
		return submoduleSource{}, errors.New("ssh submodules need the app in gitreceive SSH_CLIENT_KEY_APPS")
	}
	authority, path := raw[:colon], raw[colon+1:]
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return submoduleSource{}, errors.New("ssh URL needs a user")
	}
	user, hostname := authority[:at], authority[at+1:]
	if !validSSHUser(user) {
		return submoduleSource{}, errors.New("bad user name in URL")
	}
	if strings.ContainsAny(hostname, "[]") {
		return submoduleSource{}, errors.New("bracketed hosts are not allowed")
	}
	host, addrs, err := checkSubmoduleHost(hostname, lookup)
	if err != nil {
		return submoduleSource{}, err
	}
	return submoduleSource{Protocol: "ssh", URL: user + "@" + hostForURL(addrs[0]) + ":" + path, Pin: host}, nil
}

func validSSHUser(user string) bool {
	if user == "" || strings.HasPrefix(user, "-") {
		return false
	}
	for _, r := range user {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// checkSubmoduleHost returns the normalized host and its addresses. Every
// address must be public: one private answer (DNS rebinding, split horizon)
// refuses the host.
func checkSubmoduleHost(raw string, lookup func(string) ([]netip.Addr, error)) (string, []netip.Addr, error) {
	host := strings.TrimSuffix(strings.ToLower(raw), ".")
	if host == "" {
		return "", nil, errors.New("URL has no host")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !publicSubmoduleAddr(ip) {
			return "", nil, fmt.Errorf("address %s is not public", ip)
		}
		return host, []netip.Addr{ip.Unmap()}, nil
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", nil, fmt.Errorf("bad host name %q", raw)
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return "", nil, fmt.Errorf("bad host name %q", raw)
			}
		}
	}
	if !strings.Contains(host, ".") {
		return "", nil, fmt.Errorf("host %q is not a fully qualified name", raw)
	}
	for _, suffix := range []string{"discoverd", "localhost", "local", "internal"} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return "", nil, fmt.Errorf("host %q is internal", raw)
		}
	}
	addrs, err := lookup(host)
	if err != nil {
		return "", nil, fmt.Errorf("resolving %s: %v", host, err)
	}
	if len(addrs) == 0 {
		return "", nil, fmt.Errorf("%s has no addresses", host)
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		if !publicSubmoduleAddr(a) {
			return "", nil, fmt.Errorf("%s resolves to non-public address %s", host, a)
		}
		out = append(out, a.Unmap())
	}
	return host, out, nil
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT; Flynn's flannel overlay is 100.100.0.0/16
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64 maps to IPv4, including private ranges
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("fec0::/10"),
}

func publicSubmoduleAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

func hostForURL(a netip.Addr) string {
	if a.Is6() {
		return "[" + a.String() + "]"
	}
	return a.String()
}

func curlResolve(host, port string, addrs []netip.Addr) string {
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = hostForURL(a)
	}
	return host + ":" + port + ":" + strings.Join(parts, ",")
}
