package router

import "testing"

func TestHTTPPathRequiresClusterAdmin(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"", false},
		{"/", false},
		{"  /  ", false},
		{"/admin", true},
		{"admin", true},
		{"/foo/bar", true},
	}
	for _, tc := range cases {
		if got := HTTPPathRequiresClusterAdmin(tc.path); got != tc.want {
			t.Errorf("path %q: got %v want %v", tc.path, got, tc.want)
		}
	}
}
