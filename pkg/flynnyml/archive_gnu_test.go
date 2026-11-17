package flynnyml

import (
	"archive/tar"
	"bytes"
	"os"
	"testing"
)

func TestReadTreeDotSlashNames(t *testing.T) {
	body := []byte("build:\n  docker:\n    web: Dockerfile\n")
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: ".", Mode: 0755, Typeflag: tar.TypeDir}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "./flynn.yml", Mode: 0644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "./Dockerfile", Mode: 0644, Size: 5, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("FROM\n")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	tree, err := ReadTree(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(tree.FlynnYml, []byte("Dockerfile")) {
		t.Fatalf("yml %q", tree.FlynnYml)
	}
	if err := tree.CheckPath("Dockerfile"); err != nil {
		t.Fatal(err)
	}
}

func TestReadTreeGNUTarSample(t *testing.T) {
	raw, err := os.ReadFile("/tmp/gnu-tar.tar")
	if err != nil {
		t.Skip(err)
	}
	tree, err := ReadTree(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(tree.FlynnYml, []byte("docker")) {
		t.Fatalf("gnu tar yml %q", tree.FlynnYml)
	}
	if err := tree.CheckPath("Dockerfile"); err != nil {
		t.Fatal(err)
	}
}
