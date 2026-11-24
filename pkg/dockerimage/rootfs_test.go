package dockerimage

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	tarclient "github.com/randy-girard/flynn/tarreceive/client"
)

// rootEntry is one file in a test image rootfs: a regular file (link and
// dir empty), a directory (dir) or a symlink (link is its target).
type rootEntry struct {
	path string
	link string
	dir  bool
}

func rfile(path string) rootEntry     { return rootEntry{path: path} }
func rdir(path string) rootEntry      { return rootEntry{path: path, dir: true} }
func rlink(path, to string) rootEntry { return rootEntry{path: path, link: to} }
func writeRoot(t *testing.T, entries ...rootEntry) string {
	t.Helper()
	root := t.TempDir()
	for _, e := range entries {
		p := filepath.Join(root, e.path)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		var err error
		switch {
		case e.dir:
			err = os.MkdirAll(p, 0755)
		case e.link != "":
			err = os.Symlink(e.link, p)
		default:
			err = ioutil.WriteFile(p, []byte("#!/bin/true\n"), 0755)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// hostSentinel is a file on the host, outside any image root, so a lookup
// that follows an image symlink against the host root finds it.
func hostSentinel(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "e39-sentinel")
	if err := ioutil.WriteFile(p, []byte("host\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return p
}

// EDGE-39: a bare entrypoint is looked up in the image's own rootfs. A
// symlink in it (Alpine's /bin/sleep -> /bin/busybox, a merged-usr directory
// link) is resolved with the image root as "/", never against the host.
func TestFindExecutableFollowsSymlinkInsideRoot(t *testing.T) {
	sentinel := hostSentinel(t)
	up := strings.Repeat("../", strings.Count(sentinel, "/")+3)
	busybox := rfile("bin/busybox")
	for _, tc := range []struct {
		name    string
		entries []rootEntry
		exe     string
		want    string
	}{
		// the link path is kept, not its target: busybox picks the applet
		// from argv[0]
		{"absolute symlink", []rootEntry{busybox, rlink("bin/sh", "/bin/busybox")}, "sh", "/bin/sh"},
		{"relative symlink", []rootEntry{busybox, rlink("bin/sh", "busybox")}, "sh", "/bin/sh"},
		{"relative symlink to another dir", []rootEntry{busybox, rlink("usr/bin/env", "../../bin/busybox")}, "env", "/usr/bin/env"},
		{"absolute directory symlink", []rootEntry{rfile("opt/bb/app"), rlink("usr/local/bin", "/opt/bb")}, "app", "/usr/local/bin/app"},
		{"relative directory symlink", []rootEntry{rfile("usr/lib/x/app"), rlink("usr/sbin", "lib/x")}, "app", "/usr/sbin/app"},
		{"merged usr", []rootEntry{rfile("usr/sbin/nginx"), rlink("sbin", "usr/sbin"), rlink("bin", "usr/bin"), rdir("usr/bin")}, "nginx", "/usr/sbin/nginx"},
		{"symlink through a directory symlink", []rootEntry{rfile("usr/bin/busybox"), rlink("bin", "/usr/bin"), rlink("sbin/sh", "/bin/busybox")}, "sh", "/sbin/sh"},
		{"chain of three links", []rootEntry{busybox, rlink("bin/a", "b"), rlink("bin/b", "/usr/bin/c"), rlink("usr/bin/c", "../../bin/busybox")}, "a", "/bin/a"},
		{"dot-dot past the root stays in the root", []rootEntry{busybox, rlink("bin/up", up+"bin/busybox")}, "up", "/bin/up"},
		{"plain file", []rootEntry{busybox}, "busybox", "/bin/busybox"},
		{"non-executable file still counts", []rootEntry{rfile("bin/tool")}, "tool", "/bin/tool"},
		{"search order", []rootEntry{rfile("bin/app"), rfile("usr/local/sbin/app")}, "app", "/usr/local/sbin/app"},
		{"loop", []rootEntry{rlink("bin/a", "b"), rlink("bin/b", "/bin/a")}, "a", ""},
		{"self loop", []rootEntry{rlink("bin/a", "a")}, "a", ""},
		{"dangling", []rootEntry{rlink("bin/sh", "/bin/busybox")}, "sh", ""},
		{"absolute link to a host file", []rootEntry{busybox, rlink("bin/sh", sentinel)}, "sh", ""},
		{"dot-dot link to a host file", []rootEntry{busybox, rlink("bin/sh", up+sentinel[1:])}, "sh", ""},
		{"directory link to a host directory", []rootEntry{rlink("usr/local/bin", filepath.Dir(sentinel))}, filepath.Base(sentinel), ""},
		{"directory", []rootEntry{rdir("bin/app")}, "app", ""},
		{"symlink to a directory", []rootEntry{rdir("opt/app"), rlink("bin/app", "/opt/app")}, "app", ""},
		{"file used as a directory", []rootEntry{rfile("opt/bb"), rlink("bin/app", "/opt/bb/app")}, "app", ""},
		{"missing", []rootEntry{busybox}, "sh", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeRoot(t, tc.entries...)
			if got := findExecutableInRoot(root, tc.exe); got != tc.want {
				t.Fatalf("findExecutableInRoot(%q) = %q, want %q", tc.exe, got, tc.want)
			}
		})
	}
}

func TestResolveArgsThroughAbsoluteSymlink(t *testing.T) {
	root := writeRoot(t, rfile("bin/busybox"), rlink("bin/sleep", "/bin/busybox"))
	args := ResolveArgs(root, nil, []string{"sleep", "3600"})
	if len(args) != 2 || args[0] != "/bin/sleep" || args[1] != "3600" {
		t.Fatalf("ResolveArgs() = %#v", args)
	}
	// an absolute entrypoint is passed on as it is, not looked up
	args = ResolveArgs(root, []string{"/no/such/entry"}, []string{"x"})
	if len(args) != 2 || args[0] != "/no/such/entry" {
		t.Fatalf("ResolveArgs() = %#v", args)
	}
}

// layerTarEntry is one entry of a docker layer in a save directory fixture.
type layerTarEntry struct {
	name string
	link string // symlink target
	dir  bool
}

// writeSaveDir writes a docker save directory with one layer and the given
// image config.
func writeSaveDir(t *testing.T, config map[string]interface{}, entries ...layerTarEntry) string {
	t.Helper()
	saveDir := t.TempDir()
	f, err := os.Create(filepath.Join(saveDir, "layer.tar"))
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0755, Typeflag: tar.TypeReg}
		content := []byte("#!/bin/true\n")
		switch {
		case e.dir:
			hdr.Typeflag, content = tar.TypeDir, nil
		case e.link != "":
			hdr.Typeflag, hdr.Linkname, content = tar.TypeSymlink, e.link, nil
		}
		hdr.Size = int64(len(content))
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	data, _ := json.Marshal(map[string]interface{}{"architecture": "amd64", "os": "linux", "config": config})
	if err := ioutil.WriteFile(filepath.Join(saveDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal([]*Manifest{{Config: "config.json", Layers: []string{"layer.tar"}}})
	if err := ioutil.WriteFile(filepath.Join(saveDir, "manifest.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	return saveDir
}

// fakeTarreceive records the requests a build sends and stores any layer.
func fakeTarreceive(t *testing.T) (*tarclient.Client, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var reqs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Method == "GET" {
			http.NotFound(w, r)
			return
		}
		ioutil.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","type":"squashfs","length":1,"hashes":{"sha512_256":"00"}}`))
	}))
	t.Cleanup(srv.Close)
	return tarclient.NewClient(srv.URL, "key"), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string{}, reqs...)
	}
}

// Alpine's CMD ["sleep", ...] names /bin/sleep -> /bin/busybox: the push
// goes on with the link path as the process command.
func TestBuildFromSaveDirAcceptsSymlinkedEntrypoint(t *testing.T) {
	saveDir := writeSaveDir(t, map[string]interface{}{"Cmd": []string{"sleep", "3600"}},
		layerTarEntry{name: "bin", dir: true},
		layerTarEntry{name: "bin/busybox"},
		layerTarEntry{name: "bin/sleep", link: "/bin/busybox"},
	)
	tc, _ := fakeTarreceive(t)
	build, err := BuildFromSaveDir(tc, saveDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(build.Args) != 2 || build.Args[0] != "/bin/sleep" || build.Args[1] != "3600" {
		t.Fatalf("Args = %#v", build.Args)
	}
}

// A bare entrypoint the image does not have is refused before anything is
// uploaded, so a failed push leaves no layer behind (seen in EDGE-35). The
// image's PATH is not consulted: only the standard directories are searched.
func TestBuildFromSaveDirRefusesMissingEntrypointBeforeUpload(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  map[string]interface{}
		entries []layerTarEntry
	}{
		{"missing", map[string]interface{}{"Cmd": []string{"nginx"}}, []layerTarEntry{{name: "bin/busybox"}}},
		{"dangling", map[string]interface{}{"Entrypoint": []string{"sh"}}, []layerTarEntry{{name: "bin/sh", link: "/bin/busybox"}}},
		{"only on the image PATH", map[string]interface{}{"Cmd": []string{"app"}, "Env": []string{"PATH=/opt/x:/bin"}}, []layerTarEntry{{name: "opt/x/app"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saveDir := writeSaveDir(t, tc.config, tc.entries...)
			client, reqs := fakeTarreceive(t)
			_, err := BuildFromSaveDir(client, saveDir)
			if err == nil || !strings.Contains(err.Error(), "not found in merged rootfs") {
				t.Fatalf("err = %v", err)
			}
			if r := reqs(); len(r) != 0 {
				t.Fatalf("tarreceive requests = %v, want none", r)
			}
		})
	}
}

// An absolute entrypoint is not checked: the push goes on with it as it is.
func TestBuildFromSaveDirKeepsAbsoluteEntrypoint(t *testing.T) {
	saveDir := writeSaveDir(t, map[string]interface{}{"Entrypoint": []string{"/no/such/entry"}},
		layerTarEntry{name: "bin/busybox"})
	tc, reqs := fakeTarreceive(t)
	build, err := BuildFromSaveDir(tc, saveDir)
	if err != nil {
		t.Fatal(err)
	}
	if build.Args[0] != "/no/such/entry" || len(reqs()) != 2 {
		t.Fatalf("Args = %#v, requests %v", build.Args, reqs())
	}
}

func TestResolveInRoot(t *testing.T) {
	sentinel := hostSentinel(t)
	up := strings.Repeat("../", strings.Count(sentinel, "/")+3)
	var chain []rootEntry
	for i := 1; i < maxSymlinkHops; i++ {
		chain = append(chain, rlink(fmt.Sprintf("l/%d", i), fmt.Sprintf("/l/%d", i+1)))
	}
	// /l/1 -> ... -> /l/40 -> /bin/busybox: 40 links
	chain = append(chain, rlink(fmt.Sprintf("l/%d", maxSymlinkHops), "/bin/busybox"), rfile("bin/busybox"))
	for _, tc := range []struct {
		name    string
		entries []rootEntry
		path    string
		want    string // under root; "" for an error
		errno   syscall.Errno
	}{
		{"absolute link", []rootEntry{rfile("bin/busybox"), rlink("bin/sh", "/bin/busybox")}, "/bin/sh", "bin/busybox", 0},
		{"relative link", []rootEntry{rfile("bin/busybox"), rlink("bin/sh", "busybox")}, "/bin/sh", "bin/busybox", 0},
		{"directory links", []rootEntry{rfile("usr/bin/bash"), rlink("bin", "usr/bin"), rlink("usr/local/bin", "/bin")}, "/usr/local/bin/bash", "usr/bin/bash", 0},
		{"dot-dot after a link", []rootEntry{rfile("a/x"), rdir("b/c"), rlink("l", "/b/c")}, "/l/../../a/x", "a/x", 0},
		{"dot-dot clamps at the root", []rootEntry{rfile("bin/busybox"), rlink("bin/up", up+"bin/busybox")}, "/bin/up", "bin/busybox", 0},
		{"absolute host path stays in the root", []rootEntry{rfile(sentinel[1:])}, sentinel, sentinel[1:], 0},
		{"root", nil, "/", ".", 0},
		{"40 links", chain, "/l/1", "bin/busybox", 0},
		{"41 links", append(chain, rlink("l/x", "/l/1")), "/l/x", "", syscall.ELOOP},
		{"loop", []rootEntry{rlink("a", "b"), rlink("b", "a")}, "/a", "", syscall.ELOOP},
		{"directory loop", []rootEntry{rlink("d", "/d/x")}, "/d", "", syscall.ELOOP},
		{"host file", []rootEntry{rlink("bin/sh", sentinel)}, "/bin/sh", "", syscall.ENOENT},
		{"dot-dot to a host file", []rootEntry{rdir("bin"), rlink("bin/sh", up+sentinel[1:])}, "/bin/sh", "", syscall.ENOENT},
		{"dangling", []rootEntry{rlink("bin/sh", "busybox")}, "/bin/sh", "", syscall.ENOENT},
		{"file as directory", []rootEntry{rfile("bin/busybox")}, "/bin/busybox/x", "", syscall.ENOTDIR},
		{"file as directory through a link", []rootEntry{rfile("bin/busybox"), rlink("x", "bin/busybox")}, "/x/..", "", syscall.ENOTDIR},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeRoot(t, tc.entries...)
			got, fi, err := resolveInRoot(root, tc.path)
			if tc.want == "" {
				if err == nil || !errors.Is(err, tc.errno) {
					t.Fatalf("resolveInRoot(%q) = %q, %v; want %v", tc.path, got, err, tc.errno)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(root, tc.want); got != want || fi == nil || fi.Mode()&os.ModeSymlink != 0 {
				t.Fatalf("resolveInRoot(%q) = %q (%v), want %q", tc.path, got, fi, want)
			}
		})
	}
}
