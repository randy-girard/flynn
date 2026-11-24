//go:build !windows
// +build !windows

package archive

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// EDGE-106: nothing in a layer may create, write, link, chmod, chown or
// mknod anything outside the directory it is unpacked into. A path inside
// the layer is resolved the way the kernel resolves it in a container whose
// root is that directory: an absolute symlink target starts again at the
// root and ".." stops at the root. The links themselves are data and are
// kept as they are.

type entry struct {
	name     string
	typ      byte
	link     string
	body     string
	mode     int64
	mtime    time.Time
	uid, gid int
}

func file(name, body string) entry {
	return entry{name: name, typ: tar.TypeReg, body: body, mode: 0644}
}
func dir(name string) entry { return entry{name: name, typ: tar.TypeDir, mode: 0755} }
func symlink(name, target string) entry {
	return entry{name: name, typ: tar.TypeSymlink, link: target, mode: 0777}
}
func hardlink(name, target string) entry {
	return entry{name: name, typ: tar.TypeLink, link: target, mode: 0644}
}

func layer(t *testing.T, entries ...entry) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		mtime := e.mtime
		if mtime.IsZero() {
			mtime = time.Unix(1500000000, 0)
		}
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: e.typ,
			Linkname: e.link,
			Mode:     e.mode,
			Size:     int64(len(e.body)),
			ModTime:  mtime,
			Uid:      e.uid,
			Gid:      e.gid,
			Format:   tar.FormatPAX,
		}
		if e.typ != tar.TypeReg {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Size > 0 {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

// unpackers are the two exported entry points; every case runs on both.
var unpackers = []struct {
	name string
	fn   func(io.Reader, string, bool) error
}{
	{"UnpackFlat", UnpackFlat},
	{"Unpack", Unpack},
}

// fixture is a destination and, next to it (outside it), a sentinel
// directory holding one file that nothing may change.
type fixture struct {
	dest     string // the unpack directory (symlinks resolved)
	outside  string // a directory outside dest (absolute, symlinks resolved)
	sentinel string // outside/keep, a regular file
	keepFi   os.FileInfo
	outMtime time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{dest: filepath.Join(base, "a", "b", "dest"), outside: filepath.Join(base, "out")}
	for _, d := range []string{f.dest, f.outside} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	f.sentinel = filepath.Join(f.outside, "keep")
	if err := os.WriteFile(f.sentinel, []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Unix(1000000000, 0)
	if err := os.Chtimes(f.sentinel, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(f.outside, old, old); err != nil {
		t.Fatal(err)
	}
	if f.keepFi, err = os.Lstat(f.sentinel); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(f.outside)
	if err != nil {
		t.Fatal(err)
	}
	f.outMtime = fi.ModTime()
	return f
}

// checkOutside fails the test if anything outside dest changed: a new
// entry next to the sentinel, or the sentinel's content, mode, owner, link
// count or times, or the outside directory's mode or mtime.
func (f *fixture) checkOutside(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(f.outside)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "keep" {
		t.Errorf("outside dir now holds %v, want only [keep]", names)
	}
	data, err := os.ReadFile(f.sentinel)
	if err != nil || string(data) != "sentinel" {
		t.Errorf("sentinel content = %q, %v", data, err)
	}
	fi, err := os.Lstat(f.sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode() != f.keepFi.Mode() {
		t.Errorf("sentinel mode = %v, want %v", fi.Mode(), f.keepFi.Mode())
	}
	if !fi.ModTime().Equal(f.keepFi.ModTime()) {
		t.Errorf("sentinel mtime = %v, want %v", fi.ModTime(), f.keepFi.ModTime())
	}
	st, st0 := fi.Sys().(*syscall.Stat_t), f.keepFi.Sys().(*syscall.Stat_t)
	if st.Nlink != st0.Nlink || st.Uid != st0.Uid || st.Gid != st0.Gid {
		t.Errorf("sentinel nlink/uid/gid = %d/%d/%d, want %d/%d/%d", st.Nlink, st.Uid, st.Gid, st0.Nlink, st0.Uid, st0.Gid)
	}
	ofi, err := os.Lstat(f.outside)
	if err != nil {
		t.Fatal(err)
	}
	if !ofi.ModTime().Equal(f.outMtime) {
		t.Errorf("outside dir mtime = %v, want %v", ofi.ModTime(), f.outMtime)
	}
	if ofi.Mode().Perm() != 0755 {
		t.Errorf("outside dir mode = %v, want 0755", ofi.Mode())
	}
}

func readIn(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("want a file at %s: %v", path, err)
	}
	return string(data)
}

func wantLink(t *testing.T, path, target string) {
	t.Helper()
	got, err := os.Readlink(path)
	if err != nil {
		t.Errorf("want a symlink at %s: %v", path, err)
	}
	if got != target {
		t.Errorf("%s -> %q, want %q", path, got, target)
	}
}

// lookups are the ways a path is resolved: the portable walk everywhere,
// and openat2(RESOLVE_IN_ROOT) where the kernel has it.
func lookups() []string {
	if openat2Works() {
		return []string{"walk", "openat2"}
	}
	return []string{"walk"}
}

func useLookup(t *testing.T, name string) {
	was := openat2Off.Load()
	openat2Off.Store(name == "walk")
	t.Cleanup(func() { openat2Off.Store(was) })
}

func run(t *testing.T, fn func(t *testing.T, f *fixture, unpack func(io.Reader, string, bool) error)) {
	for _, l := range lookups() {
		for _, u := range unpackers {
			t.Run(l+"/"+u.name, func(t *testing.T) {
				useLookup(t, l)
				f := newFixture(t)
				fn(t, f, u.fn)
				f.checkOutside(t)
			})
		}
	}
}

// (1) x -> /abs/dir, then x/planted: the file lands under dest at the
// link's target, the link is kept.
func TestUnpackEntryThroughAbsoluteSymlinkStaysInside(t *testing.T) {
	run(t, func(t *testing.T, f *fixture, unpack func(io.Reader, string, bool) error) {
		err := unpack(layer(t, symlink("x", f.outside), file("x/planted", "planted")), f.dest, false)
		if err != nil {
			t.Fatalf("unpack: %v", err)
		}
		wantLink(t, filepath.Join(f.dest, "x"), f.outside)
		if got := readIn(t, filepath.Join(f.dest, f.outside, "planted")); got != "planted" {
			t.Errorf("planted = %q", got)
		}
	})
}

// (2) x -> ../../../out (escapes dest): accepted like a container would,
// and x/planted lands at dest/out/planted (".." stops at the root).
func TestUnpackEntryThroughRelativeEscapingSymlinkStaysInside(t *testing.T) {
	run(t, func(t *testing.T, f *fixture, unpack func(io.Reader, string, bool) error) {
		err := unpack(layer(t, dir("d/"), symlink("d/x", "../../../../out"), file("d/x/planted", "planted")), f.dest, false)
		if err != nil {
			t.Fatalf("unpack: %v", err)
		}
		wantLink(t, filepath.Join(f.dest, "d", "x"), "../../../../out")
		if got := readIn(t, filepath.Join(f.dest, "out", "planted")); got != "planted" {
			t.Errorf("planted = %q", got)
		}
	})
}

// (3) a chain a -> b -> /abs, a directory link further down the path, and
// missing directories behind a link are all resolved and created inside.
func TestUnpackEntryThroughChainedSymlinksStaysInside(t *testing.T) {
	run(t, func(t *testing.T, f *fixture, unpack func(io.Reader, string, bool) error) {
		err := unpack(layer(t,
			symlink("b", f.outside),
			symlink("a", "b"),
			file("a/planted", "one"),
			dir("d/"),
			symlink("d/link", "/../../.."+f.outside),
			file("d/link/sub/deeper/planted2", "two"),
		), f.dest, false)
		if err != nil {
			t.Fatalf("unpack: %v", err)
		}
		if got := readIn(t, filepath.Join(f.dest, f.outside, "planted")); got != "one" {
			t.Errorf("planted = %q", got)
		}
		if got := readIn(t, filepath.Join(f.dest, f.outside, "sub", "deeper", "planted2")); got != "two" {
			t.Errorf("planted2 = %q", got)
		}
	})
}

// (3) multi-layer flatten: the link comes from an earlier layer that is
// already in dest; the later layer's entries go through it inside dest.
func TestUnpackLaterLayerThroughEarlierLayerSymlinkStaysInside(t *testing.T) {
	run(t, func(t *testing.T, f *fixture, unpack func(io.Reader, string, bool) error) {
		if err := unpack(layer(t, symlink("x", f.outside), symlink("rel", "../../../out")), f.dest, false); err != nil {
			t.Fatalf("layer 1: %v", err)
		}
		if err := unpack(layer(t,
			file("x/planted", "abs"),
			file("rel/planted", "rel"),
			entry{name: "x/fifo", typ: tar.TypeFifo, mode: 0644},
		), f.dest, false); err != nil {
			t.Fatalf("layer 2: %v", err)
		}
		if got := readIn(t, filepath.Join(f.dest, f.outside, "planted")); got != "abs" {
			t.Fatalf("x/planted = %q", got)
		}
		if got := readIn(t, filepath.Join(f.dest, "out", "planted")); got != "rel" {
			t.Fatalf("rel/planted = %q", got)
		}
	})
}

// A symlink loop is an error, not a hang.
func TestUnpackSymlinkLoopFails(t *testing.T) {
	run(t, func(t *testing.T, f *fixture, unpack func(io.Reader, string, bool) error) {
		err := unpack(layer(t, symlink("a", "b"), symlink("b", "/a"), file("a/x", "x")), f.dest, false)
		if err == nil {
			t.Fatal("unpack through a symlink loop succeeded")
		}
	})
}

// (4) hardlinks: a target outside dest is refused, a target behind a
// symlink is looked up inside dest (and is missing there), and a hardlink
// to a symlink links the symlink itself: neither its mode nor its times are
// applied to the link's target.
func TestUnpackHardlinkCannotReachOutside(t *testing.T) {
	t.Run("dotdot target", func(t *testing.T) {
		run(t, func(t *testing.T, f *fixture, unpack func(io.Reader, string, bool) error) {
			err := unpack(layer(t, hardlink("h", "../../../out/keep")), f.dest, false)
			if err == nil {
				t.Fatal("hardlink to ../ outside dest succeeded")
			}
		})
	})
	t.Run("target behind symlink", func(t *testing.T) {
		run(t, func(t *testing.T, f *fixture, unpack func(io.Reader, string, bool) error) {
			err := unpack(layer(t, symlink("x", f.outside), hardlink("h", "x/keep")), f.dest, false)
			if err == nil {
				t.Fatal("hardlink to a file outside dest through a symlink succeeded")
			}
			if _, err := os.Lstat(filepath.Join(f.dest, "h")); err == nil {
				t.Fatal("dest/h exists")
			}
		})
	})
	t.Run("target behind symlink found inside", func(t *testing.T) {
		run(t, func(t *testing.T, f *fixture, unpack func(io.Reader, string, bool) error) {
			err := unpack(layer(t, symlink("x", f.outside), file("x/keep", "inside"), hardlink("h", "x/keep")), f.dest, false)
			if err != nil {
				t.Fatalf("unpack: %v", err)
			}
			if got := readIn(t, filepath.Join(f.dest, "h")); got != "inside" {
				t.Fatalf("h = %q", got)
			}
		})
	})
	t.Run("hardlink to symlink", func(t *testing.T) {
		run(t, func(t *testing.T, f *fixture, unpack func(io.Reader, string, bool) error) {
			// the old code checked the hardlink target relative to the
			// process's working directory; give it a regular file there
			t.Chdir(t.TempDir())
			if err := os.WriteFile("s", nil, 0644); err != nil {
				t.Fatal(err)
			}
			h := hardlink("h", "s")
			h.mode = 0777
			h.mtime = time.Unix(1600000000, 0)
			if err := unpack(layer(t, symlink("s", f.sentinel), h), f.dest, false); err != nil {
				t.Fatalf("unpack: %v", err)
			}
			if fi, err := os.Lstat(filepath.Join(f.dest, "h")); err != nil || os.SameFile(fi, f.keepFi) {
				t.Fatalf("dest/h = %v, %v: want a link to the symlink, not to the sentinel", fi, err)
			}
		})
	})
}

// (5) absolute names are re-rooted, names that leave dest with ".." are
// refused.
func TestUnpackEntryNames(t *testing.T) {
	run(t, func(t *testing.T, f *fixture, unpack func(io.Reader, string, bool) error) {
		if err := unpack(layer(t, file("/etc-x", "abs"), file("/a/../b", "b")), f.dest, false); err != nil {
			t.Fatalf("unpack: %v", err)
		}
		if got := readIn(t, filepath.Join(f.dest, "etc-x")); got != "abs" {
			t.Fatalf("etc-x = %q", got)
		}
		if got := readIn(t, filepath.Join(f.dest, "b")); got != "b" {
			t.Fatalf("b = %q", got)
		}
		for _, name := range []string{"../out/x", "a/../../out/x", "..", "../../../out/keep"} {
			if err := unpack(layer(t, file(name, "x")), f.dest, false); err == nil {
				t.Errorf("unpack of %q succeeded", name)
			}
		}
	})
}

// (6) an entry replaces an existing symlink, it is never written through:
// a file, a directory, a symlink and a fifo over x -> outside.
func TestUnpackEntryReplacesExistingSymlink(t *testing.T) {
	for _, e := range []entry{
		file("x", "new"),
		{name: "x/", typ: tar.TypeDir, mode: 0700},
		symlink("x", "elsewhere"),
		{name: "x", typ: tar.TypeFifo, mode: 0600},
	} {
		t.Run(string(e.typ), func(t *testing.T) {
			run(t, func(t *testing.T, f *fixture, unpack func(io.Reader, string, bool) error) {
				if err := unpack(layer(t, symlink("x", f.sentinel), symlink("y", f.outside)), f.dest, false); err != nil {
					t.Fatalf("layer 1: %v", err)
				}
				if err := unpack(layer(t, e), f.dest, false); err != nil {
					t.Fatalf("layer 2: %v", err)
				}
				fi, err := os.Lstat(filepath.Join(f.dest, "x"))
				if err != nil {
					if os.IsNotExist(err) && e.typ == tar.TypeFifo && runtime.GOOS != "linux" && os.Getuid() != 0 {
						return // mknod needs root here; the link is gone
					}
					t.Fatal(err)
				}
				if fi.Mode()&os.ModeSymlink != 0 && e.typ != tar.TypeSymlink {
					t.Fatalf("dest/x is still a symlink")
				}
			})
		})
	}
}

// (7) special files and setuid bits: kept, but created inside dest.
func TestUnpackSpecialFilesStayInside(t *testing.T) {
	run(t, func(t *testing.T, f *fixture, unpack func(io.Reader, string, bool) error) {
		suid := file("bin/suid", "#!/bin/sh\n")
		suid.mode = 04755
		err := unpack(layer(t,
			symlink("x", f.outside),
			entry{name: "x/fifo", typ: tar.TypeFifo, mode: 0644},
			entry{name: "x/null", typ: tar.TypeChar, mode: 0666},
			entry{name: "x/blk", typ: tar.TypeBlock, mode: 0600},
			suid,
		), f.dest, false)
		if err != nil {
			t.Fatalf("unpack: %v", err)
		}
		fi, err := os.Lstat(filepath.Join(f.dest, "bin", "suid"))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&os.ModeSetuid == 0 {
			t.Errorf("bin/suid mode = %v, want setuid kept", fi.Mode())
		}
		if runtime.GOOS == "linux" || os.Getuid() == 0 {
			fi, err := os.Lstat(filepath.Join(f.dest, f.outside, "fifo"))
			if err != nil {
				t.Fatalf("fifo: %v", err)
			}
			if fi.Mode()&os.ModeNamedPipe == 0 {
				t.Errorf("fifo mode = %v", fi.Mode())
			}
		}
	})
}

// (8) ownership, mode and times never follow a link: a directory entry
// whose path a later entry of the same tar turns into a symlink gets its
// times set at the end (the old code followed the link then), and an entry
// with an owner over x -> outside changes only dest.
func TestUnpackMetadataDoesNotFollowSymlinks(t *testing.T) {
	run(t, func(t *testing.T, f *fixture, unpack func(io.Reader, string, bool) error) {
		d := dir("d/")
		d.mtime = time.Unix(1600000000, 0)
		err := unpack(layer(t, d, symlink("d", f.outside)), f.dest, false)
		if err != nil {
			t.Fatalf("unpack: %v", err)
		}
		wantLink(t, filepath.Join(f.dest, "d"), f.outside)
	})
}

func TestUnpackLchownDoesNotFollowSymlinks(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("needs root to chown")
	}
	run(t, func(t *testing.T, f *fixture, unpack func(io.Reader, string, bool) error) {
		owned := file("x/owned", "o")
		owned.uid, owned.gid = 4242, 4243
		dd := entry{name: "x/", typ: tar.TypeDir, mode: 0755, uid: 4242, gid: 4243}
		if err := unpack(layer(t, symlink("x", f.outside), owned), f.dest, true); err != nil {
			t.Fatalf("unpack: %v", err)
		}
		fi, err := os.Lstat(filepath.Join(f.dest, f.outside, "owned"))
		if err != nil {
			t.Fatal(err)
		}
		if st := fi.Sys().(*syscall.Stat_t); st.Uid != 4242 || st.Gid != 4243 {
			t.Errorf("owned uid/gid = %d/%d", st.Uid, st.Gid)
		}
		// a directory entry over the link replaces it
		if err := unpack(layer(t, dd), f.dest, true); err != nil {
			t.Fatalf("unpack: %v", err)
		}
	})
}

// The overlay whiteouts of Unpack (a character device and the opaque
// xattr) are written inside dest too. Needs root (mknod, trusted xattrs).
func TestUnpackOverlayWhiteoutsStayInside(t *testing.T) {
	if os.Getuid() != 0 || runtime.GOOS != "linux" {
		t.Skip("needs root on linux")
	}
	f := newFixture(t)
	// overlayfs (a container's /tmp) refuses 0:0 character devices
	if err := syscall.Mknod(filepath.Join(f.dest, "probe"), syscall.S_IFCHR, 0); err != nil {
		t.Skipf("mknod of a whiteout device in %s: %v", f.dest, err)
	}
	os.Remove(filepath.Join(f.dest, "probe"))
	err := Unpack(layer(t, symlink("x", f.outside), file("x/.wh.gone", ""), file("x/.wh..wh..opq", "")), f.dest, true)
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	fi, err := os.Lstat(filepath.Join(f.dest, f.outside, "gone"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeCharDevice == 0 {
		t.Errorf("whiteout mode = %v", fi.Mode())
	}
	f.checkOutside(t)
}

// Flat whiteouts delete inside dest only, never through a link.
func TestUnpackFlatWhiteoutsStayInside(t *testing.T) {
	f := newFixture(t)
	if err := UnpackFlat(layer(t, symlink("x", f.outside), file("x/keep", "inside"), file("other", "o")), f.dest, false); err != nil {
		t.Fatal(err)
	}
	if err := UnpackFlat(layer(t, file("x/.wh.keep", ""), file("x/.wh..wh..opq", ""), file(".wh.other", "")), f.dest, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(f.dest, f.outside, "keep")); !os.IsNotExist(err) {
		t.Errorf("dest copy of keep: %v, want removed", err)
	}
	if _, err := os.Lstat(filepath.Join(f.dest, "other")); !os.IsNotExist(err) {
		t.Errorf("other: %v, want removed", err)
	}
	f.checkOutside(t)
}

// A plain image still unpacks as before: nested directories, modes, a
// relative and an absolute symlink inside the image, a hardlink, a file
// replaced by a later layer, and directory times.
func TestUnpackOrdinaryLayers(t *testing.T) {
	for _, u := range unpackers {
		t.Run(u.name, func(t *testing.T) {
			dest := t.TempDir()
			bin := dir("usr/bin/")
			bin.mtime = time.Unix(1400000000, 0)
			exe := file("usr/bin/busybox", "bb")
			exe.mode = 0755
			if err := u.fn(layer(t,
				dir("usr/"), bin, exe,
				symlink("bin", "usr/bin"),
				symlink("usr/bin/sh", "/usr/bin/busybox"),
				hardlink("usr/bin/ls", "usr/bin/busybox"),
				file("etc/motd", "v1"),
			), dest, false); err != nil {
				t.Fatal(err)
			}
			bfi, err := os.Stat(filepath.Join(dest, "usr", "bin"))
			if err != nil {
				t.Fatal(err)
			}
			if bfi.ModTime().Unix() != 1400000000 {
				t.Errorf("usr/bin mtime = %v", bfi.ModTime())
			}
			if err := u.fn(layer(t, file("etc/motd", "v2"), file("bin/new", "n")), dest, false); err != nil {
				t.Fatal(err)
			}
			if got := readIn(t, filepath.Join(dest, "etc", "motd")); got != "v2" {
				t.Errorf("motd = %q", got)
			}
			if got := readIn(t, filepath.Join(dest, "usr", "bin", "new")); got != "n" {
				t.Errorf("new = %q", got)
			}
			wantLink(t, filepath.Join(dest, "usr", "bin", "sh"), "/usr/bin/busybox")
			fi, err := os.Stat(filepath.Join(dest, "usr", "bin", "busybox"))
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != 0755 {
				t.Errorf("busybox mode = %v", fi.Mode())
			}
			ls, err := os.Stat(filepath.Join(dest, "usr", "bin", "ls"))
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(fi, ls) {
				t.Errorf("ls is not a hardlink of busybox")
			}
			if fi.ModTime().Unix() != 1500000000 {
				t.Errorf("busybox mtime = %v", fi.ModTime())
			}
		})
	}
}
