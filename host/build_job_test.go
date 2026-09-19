package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/randy-girard/flynn/host/containerinit"
	host "github.com/randy-girard/flynn/host/types"
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

func TestExposeContainerDiff(t *testing.T) {
	user := &host.Job{Metadata: map[string]string{"flynn-controller.app_name": "shop", "flynn-controller.type": "web"}}
	if exposeContainerDiff(user) {
		t.Fatal("user jobs must not receive /.container-diff")
	}
	build := &host.Job{Metadata: map[string]string{"flynn-controller.type": "dockerbuilder"}}
	if !exposeContainerDiff(build) {
		t.Fatal("build jobs need /.container-diff")
	}
	sys := &host.Job{Partition: "system", Metadata: map[string]string{"flynn-system-app": "true"}}
	if !exposeContainerDiff(sys) {
		t.Fatal("system jobs keep /.container-diff")
	}
}

func TestLockUntrustedJobStripsEscapeKnobs(t *testing.T) {
	sysadmin := []string{"CAP_SYS_ADMIN", "CAP_SYS_MODULE"}
	user := &host.Job{
		Metadata: map[string]string{"flynn-controller.app_name": "shop", "flynn-controller.type": "web"},
		Profiles: []host.JobProfile{host.JobProfileZFS},
		Config: host.ContainerConfig{
			HostNetwork:       true,
			HostPIDNamespace:  true,
			WriteableCgroups:  true,
			LinuxCapabilities: &sysadmin,
			Mounts:            []host.Mount{{Target: "/var/run/docker.sock", Location: "/var/run/docker.sock", Writeable: true}},
		},
	}
	lockUntrustedJob(user)
	if user.Config.HostNetwork || user.Config.HostPIDNamespace || user.Config.WriteableCgroups {
		t.Fatal("user jobs must not keep host net/pid or writable cgroups")
	}
	if user.Config.LinuxCapabilities != nil || len(user.Profiles) != 0 || len(user.Config.Mounts) != 0 {
		t.Fatalf("user jobs must drop caps/profiles/mounts: %+v", user.Config)
	}

	build := &host.Job{
		Metadata: map[string]string{"flynn-controller.type": "dockerbuilder"},
		Profiles: []host.JobProfile{host.JobProfileKVM},
		Config: host.ContainerConfig{
			HostNetwork:       true,
			LinuxCapabilities: &sysadmin,
			WriteableCgroups:  false,
			Mounts: []host.Mount{
				{Target: "/var/run/docker.sock", Location: "/sock"},
				{Target: "/tmp/build", Location: "/tmp/build", Writeable: true},
			},
		},
	}
	lockUntrustedJob(build)
	if build.Config.HostNetwork || len(build.Profiles) != 0 || build.Config.LinuxCapabilities != nil {
		t.Fatal("build jobs must not keep host net, device profiles, or a caller cap list")
	}
	if !build.Config.WriteableCgroups {
		t.Fatal("build jobs still need writable cgroups")
	}
	if len(build.Config.Mounts) != 1 || build.Config.Mounts[0].Target != "/tmp/build" {
		t.Fatalf("build jobs must keep safe mounts only: %+v", build.Config.Mounts)
	}

	sys := &host.Job{
		Partition: "system",
		Profiles:  []host.JobProfile{host.JobProfileZFS},
		Config:    host.ContainerConfig{HostNetwork: true, LinuxCapabilities: &sysadmin},
	}
	lockUntrustedJob(sys)
	if !sys.Config.HostNetwork || sys.Config.LinuxCapabilities == nil || len(sys.Profiles) != 1 {
		t.Fatal("system jobs must keep host-escape knobs they actually need")
	}
}

func TestUnsafeHostBind(t *testing.T) {
	cases := []struct {
		src  string
		want bool
	}{
		{"/var/run/docker.sock", true},
		{"/run/containerd/containerd.sock", true},
		{"/", true},
		{"/etc/shadow", true},
		{"/var/lib/flynn/volumes", true},
		{"/dev/sda", true},
		{"/tmp/src", false},
		{"/workspace", false},
	}
	for _, tc := range cases {
		if got := unsafeHostBind(host.Mount{Target: tc.src}); got != tc.want {
			t.Errorf("unsafeHostBind(%q)=%v want %v", tc.src, got, tc.want)
		}
	}
}

func TestCopyFileMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("flynn-init"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := copyFileMode(src, dst, 0500); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0500 {
		t.Fatalf("mode=%o", st.Mode().Perm())
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "flynn-init" {
		t.Fatalf("content=%q err=%v", got, err)
	}
}

func TestUserJobInitEnv(t *testing.T) {
	user := &host.Job{Metadata: map[string]string{"flynn-controller.app_name": "shop", "flynn-controller.type": "web"}}
	got := userJobInitEnv(user)
	if got[containerinit.HostRegistersServices] != "1" {
		t.Fatalf("user jobs must have flynn-host register services: %v", got)
	}
	if _, ok := got["DISCOVERD"]; ok {
		t.Fatalf("user jobs must not receive DISCOVERD: %v", got)
	}
	sys := &host.Job{Partition: "system", Metadata: map[string]string{"flynn-system-app": "true"}}
	if env := userJobInitEnv(sys); env != nil {
		t.Fatalf("system jobs keep in-container discoverd registration: %v", env)
	}
}
