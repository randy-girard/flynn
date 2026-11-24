package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// EDGE-27: .gitmodules sections are keyed by submodule name, and the name
// need not equal the path (`git submodule add --name lib vendor/lib`). Git
// checks a submodule out at its section's `path` and fetches its section's
// `url`. The hook asks `gitreceived -list-submodules <rev>` for the
// submodules of the pushed commit: one parse of .gitmodules
// (`git config --blob <rev>:.gitmodules --list -z`) maps every gitlink in
// the tree (`git ls-tree -r -z`) to the one section whose path is that
// gitlink, and the hook checks and fetches exactly those URLs into exactly
// those paths. Anything ambiguous or unsafe fails the push before any URL is
// checked or contacted. It prints, per gitlink, NUL-terminated
//
//	<commit> <path> <url>
//
// Only `path` and `url` are read; `update`, `branch` and other keys are
// ignored and never run.

const submoduleListFlag = "-list-submodules"

type gitlink struct {
	Path string
	SHA  string
}

type submoduleEntry struct {
	Name string
	Path string
	URL  string
	SHA  string
}

func runListSubmodules(args []string) error {
	if len(args) != 1 || args[0] == "" || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("usage: gitreceived %s <rev>", submoduleListFlag)
	}
	entries, err := listSubmodules(args[0])
	if err != nil {
		return err
	}
	w := bufio.NewWriter(os.Stdout)
	for _, e := range entries {
		fmt.Fprintf(w, "%s\x00%s\x00%s\x00", e.SHA, e.Path, e.URL)
	}
	return w.Flush()
}

func gitOutput(args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.Command("git", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%v: %s", err, msg)
		}
		return nil, err
	}
	return out, nil
}

func listSubmodules(rev string) ([]submoduleEntry, error) {
	tree, err := gitOutput("ls-tree", "-r", "-z", "--full-tree", rev)
	if err != nil {
		return nil, fmt.Errorf("listing the tree of %s: %v", rev, err)
	}
	entries, err := parseTree(tree)
	if err != nil {
		return nil, err
	}
	var links []gitlink
	modulesMode := ""
	for _, e := range entries {
		switch {
		case e.mode == "160000":
			links = append(links, gitlink{Path: e.path, SHA: e.sha})
		case e.path == ".gitmodules":
			modulesMode = e.mode
		}
	}
	if len(links) == 0 {
		return nil, nil
	}
	if modulesMode == "" {
		return planSubmodules(links, nil, false)
	}
	if modulesMode != "100644" && modulesMode != "100755" {
		return nil, errors.New(".gitmodules is not a regular file")
	}
	list, err := gitOutput("config", "--blob", rev+":.gitmodules", "--list", "-z")
	if err != nil {
		return nil, fmt.Errorf(".gitmodules cannot be read: %v", err)
	}
	return planSubmodules(links, list, true)
}

type treeEntry struct {
	mode, typ, sha, path string
}

// parseTree reads `git ls-tree -z` output: "<mode> <type> <sha>\t<path>\0".
func parseTree(b []byte) ([]treeEntry, error) {
	var out []treeEntry
	for _, rec := range bytes.Split(b, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		tab := bytes.IndexByte(rec, '\t')
		if tab < 0 {
			return nil, fmt.Errorf("unexpected ls-tree output %q", rec)
		}
		f := strings.Fields(string(rec[:tab]))
		if len(f) != 3 {
			return nil, fmt.Errorf("unexpected ls-tree output %q", rec)
		}
		out = append(out, treeEntry{mode: f[0], typ: f[1], sha: f[2], path: string(rec[tab+1:])})
	}
	return out, nil
}

// parseGitlinks returns the gitlinks (mode 160000) in `git ls-tree -z` output.
func parseGitlinks(b []byte) ([]gitlink, error) {
	entries, err := parseTree(b)
	if err != nil {
		return nil, err
	}
	var links []gitlink
	for _, e := range entries {
		if e.mode == "160000" {
			links = append(links, gitlink{Path: e.path, SHA: e.sha})
		}
	}
	return links, nil
}

type gitmodulesSection struct {
	name            string
	path, url       *string
	nPath, nURL     int
	pathNil, urlNil bool
}

// planSubmodules maps each gitlink to the .gitmodules section whose path it
// is. list is `git config --list -z` output for .gitmodules; haveModules is
// false when the commit has no .gitmodules.
func planSubmodules(links []gitlink, list []byte, haveModules bool) ([]submoduleEntry, error) {
	if len(links) == 0 {
		return nil, nil
	}
	var order []string
	sections := map[string]*gitmodulesSection{}
	for _, rec := range bytes.Split(list, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		key, value, hasValue := string(rec), "", false
		if nl := bytes.IndexByte(rec, '\n'); nl >= 0 {
			key, value, hasValue = string(rec[:nl]), string(rec[nl+1:]), true
		}
		// git prints the section and variable in lower case and the
		// subsection (the submodule name) as written.
		if !strings.HasPrefix(key, "submodule.") {
			continue
		}
		rest := key[len("submodule."):]
		dot := strings.LastIndex(rest, ".")
		if dot < 0 {
			continue
		}
		name, variable := rest[:dot], rest[dot+1:]
		if variable != "path" && variable != "url" {
			continue
		}
		s, ok := sections[name]
		if !ok {
			s = &gitmodulesSection{name: name}
			sections[name] = s
			order = append(order, name)
		}
		v := value
		if variable == "path" {
			s.nPath++
			s.path, s.pathNil = &v, !hasValue
		} else {
			s.nURL++
			s.url, s.urlNil = &v, !hasValue
		}
	}

	byPath := map[string]*gitmodulesSection{}
	for _, name := range order {
		s := sections[name]
		if !validSubmoduleName(name) {
			return nil, fmt.Errorf(".gitmodules section %q: the name is not a valid submodule name (no control characters, no \"..\" components)", name)
		}
		if s.nPath > 1 {
			return nil, fmt.Errorf(".gitmodules section %q has more than one path", name)
		}
		if s.nURL > 1 {
			return nil, fmt.Errorf(".gitmodules section %q has more than one url", name)
		}
		if s.pathNil {
			return nil, fmt.Errorf(".gitmodules section %q: path has no value", name)
		}
		if s.urlNil {
			return nil, fmt.Errorf(".gitmodules section %q: url has no value", name)
		}
		if s.path == nil {
			continue
		}
		p := *s.path
		if !safeSubmodulePath(p) {
			return nil, fmt.Errorf(".gitmodules section %q: path %q is not a safe submodule path (relative, no empty, \".\", \"..\" or .git components, no control characters)", name, p)
		}
		if other, ok := byPath[p]; ok {
			return nil, fmt.Errorf(".gitmodules sections %q and %q have the same path %q", other.name, name, p)
		}
		byPath[p] = s
	}

	out := make([]submoduleEntry, 0, len(links))
	for _, l := range links {
		if !safeSubmodulePath(l.Path) {
			return nil, fmt.Errorf("submodule %q: not a safe submodule path", l.Path)
		}
		s, ok := byPath[l.Path]
		if !ok {
			if !haveModules {
				return nil, fmt.Errorf("submodule %s: no .gitmodules section has path = %s (the commit has no .gitmodules)", l.Path, l.Path)
			}
			return nil, fmt.Errorf("submodule %s: no .gitmodules section has path = %s", l.Path, l.Path)
		}
		if s.url == nil || *s.url == "" {
			return nil, fmt.Errorf("submodule %s: .gitmodules section %q has no url", l.Path, s.name)
		}
		out = append(out, submoduleEntry{Name: s.name, Path: l.Path, URL: *s.url, SHA: l.SHA})
	}
	return out, nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// validSubmoduleName follows git's check_submodule_name (no ".." component,
// with "/" or "\" as separators) and refuses control characters.
func validSubmoduleName(name string) bool {
	if name == "" || hasControl(name) {
		return false
	}
	for _, c := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }) {
		if c == ".." {
			return false
		}
	}
	return true
}

// safeSubmodulePath accepts a path inside the work tree, spelled the way git
// stores it in a tree: relative, "/"-separated, no empty, "." or ".."
// components, no .git component (any case), no control characters.
func safeSubmodulePath(p string) bool {
	if p == "" || hasControl(p) || strings.HasPrefix(p, "/") {
		return false
	}
	for _, c := range strings.Split(p, "/") {
		if c == "" || c == "." || c == ".." || strings.EqualFold(c, ".git") {
			return false
		}
	}
	return true
}
