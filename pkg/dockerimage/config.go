package dockerimage

import (
	"strconv"
	"strings"
)

// ListenPort returns the TCP port the container listens on. It prefers
// EXPOSE from the image config, falling back to 8080 (Flynn's conventional
// platform port).
func ListenPort(exposed map[string]interface{}) int {
	if len(exposed) == 0 {
		return 8080
	}
	ports := make([]int, 0, len(exposed))
	for spec := range exposed {
		parts := strings.SplitN(spec, "/", 2)
		if len(parts) != 2 || parts[1] != "tcp" {
			continue
		}
		p, err := strconv.Atoi(parts[0])
		if err != nil || p <= 0 {
			continue
		}
		ports = append(ports, p)
	}
	if len(ports) == 0 {
		return 8080
	}
	for _, p := range ports {
		if p == 80 {
			return 80
		}
	}
	for _, p := range ports {
		if p == 8080 {
			return 8080
		}
	}
	min := ports[0]
	for _, p := range ports[1:] {
		if p < min {
			min = p
		}
	}
	return min
}

// ResolveArgs builds container command args from a Docker image config and
// resolves the entrypoint binary to an absolute path when it exists in the
// merged rootfs.
func ResolveArgs(root string, entrypoint, cmd []string) []string {
	args := append(append([]string{}, entrypoint...), cmd...)
	if len(args) == 0 || strings.Contains(args[0], "/") {
		return args
	}
	if p := findExecutableInRoot(root, args[0]); p != "" {
		args[0] = p
	}
	return args
}

// findExecutableInRoot looks a bare command name up in the standard
// directories of the image rootfs at root (not the image's PATH) and returns
// its path inside the image, or "" when none has it. A symlink on the way is
// resolved inside the image (EDGE-39: Alpine's /bin/sleep -> /bin/busybox);
// the path returned is the one searched, not the link's target, since a
// multi-call binary such as busybox picks its applet from argv[0]. Anything
// but a directory counts, as before.
func findExecutableInRoot(root, name string) string {
	for _, dir := range []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"} {
		if _, st, err := resolveInRoot(root, dir+"/"+name); err == nil && !st.IsDir() {
			return dir + "/" + name
		}
	}
	return ""
}
