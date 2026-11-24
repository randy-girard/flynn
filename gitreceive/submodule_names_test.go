package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// EDGE-27: .gitmodules sections are keyed by submodule name, and the name
// and the path can differ (`git submodule add --name lib vendor/lib`). Git
// takes the checkout location from `path` and the URL from the same
// section's `url`. The hook must do the same for every gitlink, and fail the
// push when a gitlink has no URL instead of shipping an empty directory.

// modulesParent makes a repository with the given .gitmodules text (none when
// empty) and a gitlink at each path in links, recording that commit.
func modulesParent(t *testing.T, modules string, links map[string]string) (string, string) {
	t.Helper()
	parent := t.TempDir()
	runGit(t, parent, "init", "-q", "-b", "main")
	runGit(t, parent, "config", "user.email", "t@example.com")
	runGit(t, parent, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(parent, "keep.txt"), []byte("keep\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, parent, "add", "keep.txt")
	if modules != "" {
		if err := os.WriteFile(filepath.Join(parent, ".gitmodules"), []byte(modules), 0644); err != nil {
			t.Fatal(err)
		}
		runGit(t, parent, "add", ".gitmodules")
	}
	for path, rev := range links {
		runGit(t, parent, "update-index", "--add", "--cacheinfo", "160000,"+rev+","+path)
	}
	runGit(t, parent, "commit", "-q", "-m", "parent")
	return parent, strings.TrimSpace(string(mustGit(t, parent, "rev-parse", "HEAD")))
}

// originWith makes a repository holding one file.
func originWith(t *testing.T, file string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "t@example.com")
	runGit(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, file), []byte(file+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", file)
	runGit(t, dir, "commit", "-q", "-m", file)
	return dir, strings.TrimSpace(string(mustGit(t, dir, "rev-parse", "HEAD")))
}

// fileURLChecker stands in for -check-submodule-url: it allows file:// so the
// fetch can run offline, refuses URLs containing "refuse-me", and logs every
// URL it was asked about.
func fileURLChecker(t *testing.T) (env, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "checked")
	stub := filepath.Join(dir, "check")
	script := "#!/bin/sh\n[ \"$1\" = -check-submodule-url ] || exit 2\nprintf '%s\\n' \"$2\" >> " + log + "\n" +
		"case \"$2\" in *refuse-me*) echo 'refusing submodule URL: test' >&2; exit 1 ;; esac\n" +
		"printf 'file\\n%s\\n\\n' \"$2\"\n"
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return "SUBMODULE_URL_CHECK=" + stub, log
}

func archiveList(t *testing.T, tarPath string) string {
	t.Helper()
	listed, err := exec.Command("tar", "-tf", tarPath).Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(listed)
}

func checkedURLs(t *testing.T, log string) []string {
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(data))
}

func TestSubmoduleNamedDifferentlyFromPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	sub, subRev := originWith(t, "foo.txt")
	modules := "[submodule \"libs/foo\"]\n\tpath = vendor/foo\n\turl = file://" + sub + "\n"
	parent, rev := modulesParent(t, modules, map[string]string{"vendor/foo": subRev})
	checker, _ := fileURLChecker(t)
	tarPath, out, err := runArchiveHook(t, parent, rev, []string{checker}, "app-id")
	if err != nil {
		t.Fatalf("archive: %v\n%s", err, out)
	}
	if listed := archiveList(t, tarPath); !strings.Contains(listed, "vendor/foo/foo.txt") {
		t.Errorf("archive lacks the submodule at its path:\n%s\n%s", listed, out)
	}
}

// Two sections whose names are each other's paths: each path must get the
// URL of the section that names that path, not of the section named like it.
func TestSubmoduleNamesSwappedWithPaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	subA, revA := originWith(t, "a.txt")
	subB, revB := originWith(t, "b.txt")
	modules := "[submodule \"b\"]\n\tpath = a\n\turl = file://" + subA + "\n" +
		"[submodule \"a\"]\n\tpath = b\n\turl = file://" + subB + "\n"
	parent, rev := modulesParent(t, modules, map[string]string{"a": revA, "b": revB})
	checker, _ := fileURLChecker(t)
	tarPath, out, err := runArchiveHook(t, parent, rev, []string{checker}, "app-id")
	if err != nil {
		t.Fatalf("archive: %v\n%s", err, out)
	}
	listed := archiveList(t, tarPath)
	for _, f := range []string{"a/a.txt", "b/b.txt"} {
		if !strings.Contains(listed, f) {
			t.Errorf("archive missing %s:\n%s\n%s", f, listed, out)
		}
	}
}

// A section named like the gitlink's path but pointing elsewhere is a decoy:
// the URL that is checked and fetched is the one from the section whose
// path is the gitlink.
func TestSubmoduleDecoySectionNamedLikePath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	decoy, _ := originWith(t, "decoy.txt")
	real, realRev := originWith(t, "real.txt")
	modules := "[submodule \"dep\"]\n\tpath = elsewhere\n\turl = file://" + decoy + "\n" +
		"[submodule \"real\"]\n\tpath = dep\n\turl = file://" + real + "\n"
	parent, rev := modulesParent(t, modules, map[string]string{"dep": realRev})
	checker, log := fileURLChecker(t)
	tarPath, out, err := runArchiveHook(t, parent, rev, []string{checker}, "app-id")
	if err != nil {
		t.Fatalf("archive: %v\n%s", err, out)
	}
	if listed := archiveList(t, tarPath); !strings.Contains(listed, "dep/real.txt") {
		t.Errorf("archive lacks dep/real.txt:\n%s\n%s", listed, out)
	}
	if got := checkedURLs(t, log); len(got) != 1 || got[0] != "file://"+real {
		t.Errorf("checked URLs %v, want only file://%s", got, real)
	}
}

// git ls-tree quotes unusual paths (non-ASCII, tabs, quotes), so a lookup by
// the printed path finds nothing even when the name equals the path.
func TestSubmoduleUnusualPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	sub, subRev := originWith(t, "u.txt")
	for _, path := range []string{"vendor/日本語", "vendor/with space", "vendor/q\"uote"} {
		quoted := strings.ReplaceAll(path, "\"", "\\\"")
		modules := "[submodule \"" + quoted + "\"]\n\tpath = \"" + quoted + "\"\n\turl = file://" + sub + "\n"
		parent, rev := modulesParent(t, modules, map[string]string{path: subRev})
		checker, _ := fileURLChecker(t)
		tarPath, out, err := runArchiveHook(t, parent, rev, []string{checker}, "app-id")
		if err != nil {
			t.Errorf("%q: archive: %v\n%s", path, err, out)
			continue
		}
		if listed := archiveList(t, tarPath); !strings.Contains(listed, path+"/u.txt") {
			t.Errorf("%q: archive lacks the submodule:\n%s\n%s", path, listed, out)
		}
	}
}

// The push fails, naming the problem, before any URL is checked or fetched
// when .gitmodules cannot be mapped onto the gitlinks unambiguously.
func TestSubmoduleBadGitmodulesFailsPush(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	sub, subRev := originWith(t, "x.txt")
	url := "file://" + sub
	for _, c := range []struct {
		name, modules, want string
		links               map[string]string
	}{
		{"no section for gitlink", "[submodule \"other\"]\n\tpath = other\n\turl = " + url + "\n", "submodule dep: no .gitmodules section", map[string]string{"dep": subRev}},
		{"no .gitmodules", "", "submodule dep: no .gitmodules section", map[string]string{"dep": subRev}},
		{"section without url", "[submodule \"dep\"]\n\tpath = dep\n", "submodule dep: .gitmodules section \"dep\" has no url", map[string]string{"dep": subRev}},
		{"two urls", "[submodule \"dep\"]\n\tpath = dep\n\turl = " + url + "\n\turl = " + url + "x\n", "more than one url", map[string]string{"dep": subRev}},
		{"two sections, one path", "[submodule \"a\"]\n\tpath = dep\n\turl = " + url + "\n[submodule \"b\"]\n\tpath = dep\n\turl = " + url + "\n", "same path", map[string]string{"dep": subRev}},
		{"dotdot path", "[submodule \"dep\"]\n\tpath = ../dep\n\turl = " + url + "\n[submodule \"ok\"]\n\tpath = dep\n\turl = " + url + "\n", "\"../dep\"", map[string]string{"dep": subRev}},
		{"absolute path", "[submodule \"dep\"]\n\tpath = /etc/dep\n\turl = " + url + "\n[submodule \"ok\"]\n\tpath = dep\n\turl = " + url + "\n", "\"/etc/dep\"", map[string]string{"dep": subRev}},
		{"dotgit path", "[submodule \"dep\"]\n\tpath = x/.GIT\n\turl = " + url + "\n[submodule \"ok\"]\n\tpath = dep\n\turl = " + url + "\n", "\"x/.GIT\"", map[string]string{"dep": subRev}},
	} {
		parent, rev := modulesParent(t, c.modules, c.links)
		checker, log := fileURLChecker(t)
		tarPath, out, err := runArchiveHook(t, parent, rev, []string{checker}, "app-id")
		if err == nil {
			t.Errorf("%s: hook succeeded\n%s", c.name, out)
		}
		if !strings.Contains(string(out), c.want) {
			t.Errorf("%s: output lacks %q:\n%s", c.name, c.want, out)
		}
		if got := checkedURLs(t, log); len(got) != 0 {
			t.Errorf("%s: URLs were checked before the .gitmodules error: %v", c.name, got)
		}
		if listed, _ := exec.Command("tar", "-tf", tarPath).Output(); strings.Contains(string(listed), "x.txt") {
			t.Errorf("%s: archive contains the submodule", c.name)
		}
	}
}

// Every submodule URL is checked before any submodule is fetched, so a
// refused second submodule means the first one's host is never contacted.
func TestSubmoduleAllURLsCheckedBeforeAnyFetch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	sub, subRev := originWith(t, "x.txt")
	pathEnv, marker := fakeSSHTools(t)
	home := sshHome(t, "203.0.113.5 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITEST\n")
	modules := "[submodule \"a\"]\n\tpath = a\n\turl = ssh://git@203.0.113.5" + sub + "\n" +
		"[submodule \"b\"]\n\tpath = b\n\turl = file://" + sub + "\n"
	parent, rev := modulesParent(t, modules, map[string]string{"a": subRev, "b": subRev})
	_, out, err := runArchiveHook(t, parent, rev, []string{
		pathEnv, "HOME=" + home, "SSH_CLIENT_KEY_APPS=app-name",
		"SSH_CLIENT_KEY=" + testClientKey, "SUBMODULE_KEY_DIR=" + t.TempDir(),
	}, "app-id", "app-name")
	if err == nil {
		t.Fatalf("hook succeeded\n%s", out)
	}
	if !strings.Contains(string(out), "submodule b: cannot fetch file://") {
		t.Errorf("missing refusal for b:\n%s", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("submodule a was fetched although submodule b is refused")
	}
}

// gitreceive reads only path and url: `update = !cmd` and other keys never
// run anything.
func TestSubmoduleUpdateCommandIgnored(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	sub, subRev := originWith(t, "x.txt")
	touched := filepath.Join(t.TempDir(), "touched")
	modules := "[submodule \"dep\"]\n\tpath = dep\n\turl = file://" + sub + "\n\tupdate = !touch " + touched + "\n"
	parent, rev := modulesParent(t, modules, map[string]string{"dep": subRev})
	checker, _ := fileURLChecker(t)
	tarPath, out, err := runArchiveHook(t, parent, rev, []string{checker}, "app-id")
	if err != nil {
		t.Fatalf("archive: %v\n%s", err, out)
	}
	if !strings.Contains(archiveList(t, tarPath), "dep/x.txt") {
		t.Errorf("archive lacks dep/x.txt\n%s", out)
	}
	if _, err := os.Stat(touched); err == nil {
		t.Error("update = !cmd ran")
	}
}

// configList is what `git config --blob <rev>:.gitmodules --list -z` prints
// for the given .gitmodules text.
func configList(t *testing.T, modules string) []byte {
	t.Helper()
	f := filepath.Join(t.TempDir(), "gitmodules")
	if err := os.WriteFile(f, []byte(modules), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "config", "-f", f, "--list", "-z").Output()
	if err != nil {
		t.Fatalf("git config: %v", err)
	}
	return out
}

func TestPlanSubmodules(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	const u = "https://github.com/octocat/Hello-World.git"
	ok := []struct {
		name, modules string
		links         []string
		want          []string // name=path=url per entry, in link order
	}{
		{"name differs", "[submodule \"libs/foo\"]\n\tpath = vendor/foo\n\turl = " + u + "\n", []string{"vendor/foo"}, []string{"libs/foo=vendor/foo=" + u}},
		{"dots in name", "[submodule \"a.b.c\"]\n\tpath = x\n\turl = " + u + "\n", []string{"x"}, []string{"a.b.c=x=" + u}},
		{"quote in name", "[submodule \"we\\\"ird\"]\n\tpath = x\n\turl = " + u + "\n", []string{"x"}, []string{"we\"ird=x=" + u}},
		{"key case", "[submodule \"Dep\"]\n\tPATH = dep\n\tURL = " + u + "\n", []string{"dep"}, []string{"Dep=dep=" + u}},
		{"old-style section", "[submodule.Dep]\n\tpath = dep\n\turl = " + u + "\n", []string{"dep"}, []string{"dep=dep=" + u}},
		{"swapped", "[submodule \"b\"]\n\tpath = a\n\turl = " + u + "/a\n[submodule \"a\"]\n\tpath = b\n\turl = " + u + "/b\n", []string{"a", "b"}, []string{"b=a=" + u + "/a", "a=b=" + u + "/b"}},
		{"extra keys and sections", "[core]\n\tbare = true\n[submodule \"dep\"]\n\tpath = dep\n\turl = " + u + "\n\tupdate = !rm -rf /\n\tbranch = main\n[submodule \"stale\"]\n\turl = file:///etc\n", []string{"dep"}, []string{"dep=dep=" + u}},
		{"section split in two", "[submodule \"dep\"]\n\tpath = dep\n[submodule \"dep\"]\n\turl = " + u + "\n", []string{"dep"}, []string{"dep=dep=" + u}},
		{"no gitlinks", "garbage", nil, nil},
	}
	for _, c := range ok {
		var links []gitlink
		for _, p := range c.links {
			links = append(links, gitlink{Path: p, SHA: "1111111111111111111111111111111111111111"})
		}
		var list []byte
		if c.links != nil {
			list = configList(t, c.modules)
		}
		got, err := planSubmodules(links, list, c.links != nil)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		var s []string
		for _, e := range got {
			s = append(s, e.Name+"="+e.Path+"="+e.URL)
		}
		if strings.Join(s, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s: got %q, want %q", c.name, s, c.want)
		}
	}

	bad := []struct {
		name, modules, link, want string
	}{
		{"no section", "[submodule \"other\"]\n\tpath = other\n\turl = " + u + "\n", "dep", "no .gitmodules section"},
		{"no url", "[submodule \"dep\"]\n\tpath = dep\n", "dep", "has no url"},
		{"empty url", "[submodule \"dep\"]\n\tpath = dep\n\turl =\n", "dep", "has no url"},
		{"bool path", "[submodule \"dep\"]\n\tpath\n\turl = " + u + "\n", "dep", "has no value"},
		{"two urls", "[submodule \"dep\"]\n\tpath = dep\n\turl = " + u + "\n\turl = " + u + "x\n", "dep", "more than one url"},
		{"two paths", "[submodule \"dep\"]\n\tpath = dep\n\tpath = other\n\turl = " + u + "\n", "dep", "more than one path"},
		{"same path twice", "[submodule \"a\"]\n\tpath = dep\n\turl = " + u + "\n[submodule \"b\"]\n\tpath = dep\n\turl = " + u + "\n", "dep", "same path"},
		{"dotdot", "[submodule \"dep\"]\n\tpath = a/../dep\n\turl = " + u + "\n", "dep", "not a safe submodule path"},
		{"leading dotdot", "[submodule \"dep\"]\n\tpath = ../dep\n\turl = " + u + "\n", "dep", "not a safe submodule path"},
		{"absolute", "[submodule \"dep\"]\n\tpath = /dep\n\turl = " + u + "\n", "dep", "not a safe submodule path"},
		{"dot", "[submodule \"dep\"]\n\tpath = ./dep\n\turl = " + u + "\n", "dep", "not a safe submodule path"},
		{"trailing slash", "[submodule \"dep\"]\n\tpath = dep/\n\turl = " + u + "\n", "dep", "not a safe submodule path"},
		{"dotgit", "[submodule \"dep\"]\n\tpath = .git\n\turl = " + u + "\n", "dep", "not a safe submodule path"},
		{"dotgit case", "[submodule \"dep\"]\n\tpath = x/.Git/y\n\turl = " + u + "\n", "dep", "not a safe submodule path"},
		{"tab in path", "[submodule \"dep\"]\n\tpath = \"de\\tp\"\n\turl = " + u + "\n", "dep", "not a safe submodule path"},
		{"newline in path", "[submodule \"dep\"]\n\tpath = \"de\\np\"\n\turl = " + u + "\n", "dep", "not a safe submodule path"},
		{"dotdot name", "[submodule \"../x\"]\n\tpath = dep\n\turl = " + u + "\n", "dep", "not a valid submodule name"},
		{"control name", "[submodule \"a\x01b\"]\n\tpath = dep\n\turl = " + u + "\n", "dep", "not a valid submodule name"},
		{"unsafe gitlink path", "[submodule \"dep\"]\n\tpath = ../dep\n\turl = " + u + "\n", "../dep", "not a safe submodule path"},
	}
	for _, c := range bad {
		links := []gitlink{{Path: c.link, SHA: "1111111111111111111111111111111111111111"}}
		got, err := planSubmodules(links, configList(t, c.modules), true)
		if err == nil {
			t.Errorf("%s: accepted %+v", c.name, got)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q lacks %q", c.name, err, c.want)
		}
	}
}

// parseGitlinks reads `git ls-tree -r -z --full-tree` output.
func TestParseGitlinks(t *testing.T) {
	in := "100644 blob aaaa\tkeep.txt\x00160000 commit bbbb\tvendor/h\xc3\xa9llo\x00160000 commit cccc\twith\ttab\x00040000 tree dddd\tdir\x00"
	got, err := parseGitlinks([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, l := range got {
		s = append(s, l.SHA+" "+l.Path)
	}
	sort.Strings(s)
	if strings.Join(s, "|") != "bbbb vendor/h\xc3\xa9llo|cccc with\ttab" {
		t.Errorf("got %q", s)
	}
}

// A relative submodule URL resolves against the superproject's remote in
// git, which gitreceive does not have; the refusal says so.
func TestCheckSubmoduleURLRelative(t *testing.T) {
	for _, raw := range []string{"../lib.git", "./lib"} {
		_, err := checkSubmoduleURL(raw, true, fakeLookup(nil))
		if err == nil || !strings.Contains(err.Error(), "relative submodule URLs are not supported") {
			t.Errorf("%q: got %v", raw, err)
		}
	}
}
