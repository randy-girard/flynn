package flynnyml

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"path"
	"strings"
)

// Tree is a git archive index. FlynnYml is the root file when it is present.
type Tree struct {
	FlynnYml   []byte
	HasAppJSON bool
	index      map[string]tarEntry
}

type tarEntry struct {
	kind byte
	link string
	size int64
}

// ReadTree indexes a git archive. A flynn.yml or Dockerfile symlink whose
// target leaves the repository is an error. The reader is rewound when it
// supports seeking.
func ReadTree(r io.Reader) (*Tree, error) {
	rs, err := asReadSeeker(r)
	if err != nil {
		return nil, err
	}
	tree, err := indexTar(rs)
	if err != nil {
		return nil, err
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if err := tree.noteAppJSON(); err != nil {
		return nil, err
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if err := tree.loadFlynnYml(rs); err != nil {
		return nil, err
	}
	return tree, nil
}

// CheckPath fails when path is missing, not a regular file, or a symlink
// that leaves the repository.
func (t *Tree) CheckPath(p string) error {
	if t == nil {
		return fmt.Errorf("flynn.yml: %s not found", p)
	}
	resolved, err := t.resolve(p, 0)
	if err != nil {
		return err
	}
	e, ok := t.index[resolved]
	if !ok || e.kind != 'f' {
		return fmt.Errorf("flynn.yml: %s is not a file", p)
	}
	return nil
}

func asReadSeeker(r io.Reader) (io.ReadSeeker, error) {
	if rs, ok := r.(io.ReadSeeker); ok {
		if _, err := rs.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		return rs, nil
	}
	buf := &bytes.Buffer{}
	if _, err := io.Copy(buf, r); err != nil {
		return nil, err
	}
	return bytes.NewReader(buf.Bytes()), nil
}

func indexTar(r io.Reader) (*Tree, error) {
	tr := tar.NewReader(r)
	tree := &Tree{index: map[string]tarEntry{}}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name, ok := tarPath(hdr.Name)
		if !ok {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			tree.index[name] = tarEntry{kind: 'f', size: hdr.Size}
		case tar.TypeDir:
			tree.index[name] = tarEntry{kind: 'd'}
		case tar.TypeSymlink, tar.TypeLink:
			tree.index[name] = tarEntry{kind: 'l', link: hdr.Linkname}
		}
	}
	return tree, nil
}

func (t *Tree) noteAppJSON() error {
	e, ok := t.index["flynn-app.json"]
	if !ok {
		return nil
	}
	if e.kind == 'l' {
		resolved, err := t.resolve("flynn-app.json", 0)
		if err != nil {
			return err
		}
		e = t.index[resolved]
	}
	t.HasAppJSON = e.kind == 'f'
	return nil
}

func (t *Tree) loadFlynnYml(r io.Reader) error {
	e, ok := t.index["flynn.yml"]
	if !ok {
		return nil
	}
	target := "flynn.yml"
	if e.kind == 'l' {
		resolved, err := t.resolve("flynn.yml", 0)
		if err != nil {
			return err
		}
		target = resolved
		e = t.index[target]
	}
	if e.kind != 'f' {
		return fmt.Errorf("flynn.yml: flynn.yml is not a file")
	}
	if e.size > MaxBytes {
		return fmt.Errorf("flynn.yml is larger than %d bytes", MaxBytes)
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name, ok := tarPath(hdr.Name)
		if !ok || name != target || hdr.FileInfo().IsDir() {
			continue
		}
		if hdr.Size > MaxBytes {
			return fmt.Errorf("flynn.yml is larger than %d bytes", MaxBytes)
		}
		body, err := io.ReadAll(io.LimitReader(tr, MaxBytes+1))
		if err != nil {
			return err
		}
		if int64(len(body)) > MaxBytes {
			return fmt.Errorf("flynn.yml is larger than %d bytes", MaxBytes)
		}
		t.FlynnYml = body
		return nil
	}
	return fmt.Errorf("flynn.yml: %s not found", target)
}

func (t *Tree) resolve(p string, depth int) (string, error) {
	if depth > 8 {
		return "", fmt.Errorf("flynn.yml: symlink loop in %s", p)
	}
	cleaned, err := cleanRepoPath(p)
	if err != nil {
		return "", err
	}
	parts := strings.Split(cleaned, "/")
	cur := ""
	for i, part := range parts {
		if cur == "" {
			cur = part
		} else {
			cur = cur + "/" + part
		}
		e, ok := t.index[cur]
		if !ok {
			if i == len(parts)-1 {
				return "", fmt.Errorf("flynn.yml: %s not found", p)
			}
			continue
		}
		if e.kind != 'l' {
			continue
		}
		target, err := linkInside(path.Dir(cur), e.link)
		if err != nil {
			return "", err
		}
		if rest := strings.Join(parts[i+1:], "/"); rest != "" {
			target = target + "/" + rest
		}
		return t.resolve(target, depth+1)
	}
	return cleaned, nil
}

func linkInside(dir, link string) (string, error) {
	link = strings.TrimSpace(link)
	if link == "" || strings.HasPrefix(link, "/") || strings.ContainsRune(link, 0) {
		return "", fmt.Errorf("flynn.yml: symlink escapes the repository")
	}
	joined := link
	if dir != "" && dir != "." {
		joined = dir + "/" + link
	}
	cleaned := path.Clean(joined)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("flynn.yml: symlink escapes the repository")
	}
	return cleaned, nil
}

func tarPath(name string) (string, bool) {
	name = strings.TrimPrefix(name, "./")
	name = strings.TrimPrefix(name, "/")
	if name == "" || strings.HasSuffix(name, "/") || strings.ContainsRune(name, 0) {
		return "", false
	}
	return path.Clean(name), true
}
