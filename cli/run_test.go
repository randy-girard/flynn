package main

import (
	"reflect"
	"testing"

	ct "github.com/flynn/flynn/controller/types"
)

func TestRunJobArgs(t *testing.T) {
	slug := &ct.Release{Meta: map[string]string{"git": "true", "slugrunner.stack": "heroku-24"}}
	legacySlug := &ct.Release{Meta: map[string]string{"git": "true"}}
	container := &ct.Release{Meta: map[string]string{"git": "true", "slugrunner.stack": "container"}}
	dockerRecv := &ct.Release{Meta: map[string]string{"docker-receive": "true"}}

	cases := []struct {
		name    string
		release *ct.Release
		args    []string
		want    []string
	}{
		{
			name:    "slug prepends runner init",
			release: slug,
			args:    []string{"echo", "smoke-cli"},
			want:    []string{"/runner/init", "echo", "smoke-cli"},
		},
		{
			name:    "legacy slug without stack meta",
			release: legacySlug,
			args:    []string{"echo", "ok"},
			want:    []string{"/runner/init", "echo", "ok"},
		},
		{
			name:    "slug empty args still prepends",
			release: slug,
			args:    nil,
			want:    []string{"/runner/init"},
		},
		{
			name:    "slug already has runner init",
			release: slug,
			args:    []string{"/runner/init", "bash"},
			want:    []string{"/runner/init", "bash"},
		},
		{
			name:    "container stack runs image as-is",
			release: container,
			args:    []string{"echo", "docker-cli"},
			want:    []string{"echo", "docker-cli"},
		},
		{
			name:    "container stack empty args",
			release: container,
			args:    nil,
			want:    nil,
		},
		{
			name:    "docker receive runs image as-is",
			release: dockerRecv,
			args:    []string{"echo", "ok"},
			want:    []string{"echo", "ok"},
		},
	}
	for _, tc := range cases {
		got := runJobArgs(tc.release, tc.args)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %#v, want %#v", tc.name, got, tc.want)
		}
	}
}
