package main

import (
	"testing"

	host "github.com/flynn/flynn/host/types"
)

func TestIsBuildJob(t *testing.T) {
	cases := []struct {
		name string
		meta map[string]string
		want bool
	}{
		{name: "nil metadata", meta: nil, want: false},
		{name: "builder app", meta: map[string]string{"flynn-controller.app_name": "builder"}, want: true},
		{name: "slugbuilder", meta: map[string]string{"flynn-controller.type": "slugbuilder"}, want: true},
		{name: "dockerbuilder", meta: map[string]string{"flynn-controller.type": "dockerbuilder"}, want: true},
		{name: "regular app", meta: map[string]string{"flynn-controller.app_name": "myapp", "flynn-controller.type": "web"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBuildJob(&host.Job{Metadata: tc.meta}); got != tc.want {
				t.Fatalf("isBuildJob = %v, want %v", got, tc.want)
			}
		})
	}
}
