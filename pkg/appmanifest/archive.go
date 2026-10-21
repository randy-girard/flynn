package appmanifest

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"strings"
	"time"
)

// Spool copies r to a temp file positioned at the start.
func Spool(r io.Reader) (*os.File, error) {
	f, err := os.CreateTemp("", "flynn-app-json-")
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	return f, nil
}

// RootFiles reads the git archive once. manifest is the root flynn-app.json
// bytes (nil when absent). hasBuildpacks is true when .buildpacks is at the
// repo root.
func RootFiles(r io.Reader) (manifest []byte, hasBuildpacks bool, err error) {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, err
		}
		name := rootName(hdr.Name)
		switch name {
		case "flynn-app.json":
			if hdr.FileInfo().IsDir() {
				continue
			}
			body, err := io.ReadAll(tr)
			if err != nil {
				return nil, false, err
			}
			if len(bytes.TrimSpace(body)) > 0 {
				manifest = body
			}
		case ".buildpacks":
			if !hdr.FileInfo().IsDir() {
				hasBuildpacks = true
			}
		}
	}
	return manifest, hasBuildpacks, nil
}

// InjectRootFile copies the archive and adds name at the root when it is not
// already there.
func InjectRootFile(r io.Reader, w io.Writer, name string, body []byte) error {
	tr := tar.NewReader(r)
	tw := tar.NewWriter(w)
	defer tw.Close()
	seen := false
	want := rootName(name)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if rootName(hdr.Name) == want {
			seen = true
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}
	if seen {
		return nil
	}
	hdr := &tar.Header{
		Name:    want,
		Mode:    0644,
		ModTime: time.Unix(0, 0),
		Size:    int64(len(body)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(body)
	return err
}

func rootName(name string) string {
	name = strings.TrimPrefix(name, "./")
	name = strings.TrimPrefix(name, "/")
	if strings.Contains(name, "/") {
		return ""
	}
	return name
}
