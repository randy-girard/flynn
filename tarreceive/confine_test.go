package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// EDGE-106: tarreceive unpacks a build-token holder's tar as root. A
// symlink in it must not let a later entry write outside the upload's own
// extract directory, for example into another upload's extract directory
// before that one is squashed. The link is kept and the entry lands inside
// the extract directory at the link's target, as in the container.
func TestCreateLayerCannotWriteOutsideExtractDir(t *testing.T) {
	for _, c := range []struct{ name, link, entry string }{
		// x -> <other>, then x/bin/planted
		{"absolute link", "", "bin/planted"},
		// up -> ../../.. (above the root), then up<other>/planted
		{"relative link", "../../../../../../../../..", "planted"},
	} {
		for _, flat := range []bool{true, false} {
			c, flat := c, flat
			name := c.name + "/layer"
			if flat {
				name = c.name + "/flat"
			}
			t.Run(name, func(t *testing.T) {
				s, tok := buildServer(t)
				s.tempDir = t.TempDir()
				var up recordedUploads
				s.upload = up.upload

				// another upload's extract directory, next to this one's
				other, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				other = filepath.Join(other, "extract")
				if err := os.MkdirAll(other, 0755); err != nil {
					t.Fatal(err)
				}

				var squashed []string
				s.squash = func(src, dst string) error {
					filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
						if err == nil {
							rel, _ := filepath.Rel(src, p)
							squashed = append(squashed, rel)
						}
						return nil
					})
					return fakeSquash(src, dst)
				}

				var buf bytes.Buffer
				tw := tar.NewWriter(&buf)
				link, entry := other, "x/"+c.entry
				if c.link != "" {
					link, entry = c.link, "x"+other+"/"+c.entry
				}
				for _, h := range []*tar.Header{
					{Name: "x", Typeflag: tar.TypeSymlink, Linkname: link, Mode: 0777},
					{Name: entry, Typeflag: tar.TypeReg, Mode: 0755, Size: 7},
				} {
					h.Uid, h.Gid = os.Getuid(), os.Getgid()
					if err := tw.WriteHeader(h); err != nil {
						t.Fatal(err)
					}
					if h.Size > 0 {
						tw.Write([]byte("planted"))
					}
				}
				if err := tw.Close(); err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(buf.Bytes())
				id := hex.EncodeToString(sum[:])

				rec := postLayer(s, "/layer/"+id, buf.Bytes(), func(r *http.Request) {
					bearer(tok)(r)
					if flat {
						r.Header.Set("X-Flynn-Flattened-Layer", "1")
					}
				})
				if rec.Code != http.StatusOK {
					t.Fatalf("POST layer = %d %s", rec.Code, rec.Body.String())
				}
				if entries, err := os.ReadDir(other); err != nil || len(entries) != 0 {
					var names []string
					for _, e := range entries {
						names = append(names, e.Name())
					}
					t.Fatalf("the other extract directory now holds %v (%v), want nothing", names, err)
				}
				want := map[string]bool{"x": true, filepath.Join(other[1:], c.entry): true}
				for _, p := range squashed {
					delete(want, p)
				}
				if len(want) != 0 {
					t.Fatalf("squashed tree %v is missing %v", squashed, want)
				}
			})
		}
	}
}
