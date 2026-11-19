package main

import (
	"errors"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

type fakeCreator struct {
	err   error
	calls int
}

func (f *fakeCreator) CreateArtifact(a *ct.Artifact) error {
	f.calls++
	return f.err
}

func TestRunReturnsCreateArtifactError(t *testing.T) {
	boom := errors.New("controller rejected artifact")
	art := &ct.Artifact{}

	t.Run("cluster key client", func(t *testing.T) {
		c := &fakeCreator{err: boom}
		if err := createArtifact(c, false, nil, art); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
	})

	t.Run("token client, no fallback available", func(t *testing.T) {
		c := &fakeCreator{err: boom}
		fb := func() (artifactCreator, error) { return nil, errors.New("missing build credential") }
		if err := createArtifact(c, true, fb, art); err == nil {
			t.Fatal("expected error when token create fails and no fallback exists")
		}
	})

	t.Run("token client, fallback also fails", func(t *testing.T) {
		c := &fakeCreator{err: boom}
		fbErr := errors.New("fallback rejected")
		fb := func() (artifactCreator, error) { return &fakeCreator{err: fbErr}, nil }
		if err := createArtifact(c, true, fb, art); !errors.Is(err, fbErr) {
			t.Fatalf("err = %v, want %v", err, fbErr)
		}
	})

	t.Run("token client, fallback succeeds", func(t *testing.T) {
		c := &fakeCreator{err: boom}
		f := &fakeCreator{}
		fb := func() (artifactCreator, error) { return f, nil }
		if err := createArtifact(c, true, fb, art); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.calls != 1 {
			t.Fatalf("fallback calls = %d, want 1", f.calls)
		}
	})

	t.Run("success", func(t *testing.T) {
		c := &fakeCreator{}
		if err := createArtifact(c, true, nil, art); err != nil || c.calls != 1 {
			t.Fatalf("err=%v calls=%d", err, c.calls)
		}
	})
}
