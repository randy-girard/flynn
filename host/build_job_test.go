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

func TestBuildJobExtraCapabilities(t *testing.T) {
	caps := buildJobExtraCapabilities()
	want := map[string]bool{
		"CAP_MKNOD":      true,
		"CAP_SYS_CHROOT": true,
		"CAP_SYS_ADMIN":  true,
		"CAP_NET_ADMIN":  true,
		"CAP_NET_RAW":    true,
	}
	if len(caps) != len(want) {
		t.Fatalf("buildJobExtraCapabilities = %v, want %d caps", caps, len(want))
	}
	for _, c := range caps {
		if !want[c] {
			t.Fatalf("unexpected capability %q", c)
		}
	}
}

func TestCgroupsReadonly(t *testing.T) {
	cases := []struct {
		name                                      string
		isBuild, writeable, systemApp, systemPart bool
		want                                      bool
	}{
		{name: "build job is writable", isBuild: true, want: false},
		{name: "plain app is readonly", want: true},
		{name: "writeable but not system => readonly", writeable: true, want: true},
		{name: "writeable system app => writable", writeable: true, systemApp: true, want: false},
		{name: "writeable system partition => writable", writeable: true, systemPart: true, want: false},
		{name: "system app without writeable => readonly", systemApp: true, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cgroupsReadonly(tc.isBuild, tc.writeable, tc.systemApp, tc.systemPart); got != tc.want {
				t.Fatalf("cgroupsReadonly = %v, want %v", got, tc.want)
			}
		})
	}
}
