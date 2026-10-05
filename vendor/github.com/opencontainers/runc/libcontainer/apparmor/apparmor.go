// +build apparmor,linux

package apparmor

import (
	"fmt"
	"io/ioutil"
	"os"
	"strings"
)

// IsEnabled returns true if apparmor is enabled for the host.
func IsEnabled() bool {
	if _, err := os.Stat("/sys/kernel/security/apparmor"); err == nil && os.Getenv("container") == "" {
		if _, err = os.Stat("/sbin/apparmor_parser"); err == nil {
			buf, err := ioutil.ReadFile("/sys/module/apparmor/parameters/enabled")
			return err == nil && len(buf) > 1 && buf[0] == 'Y'
		}
	}
	return false
}

func setprocattr(attr, value string) error {
	// Under AppArmor you can only change your own attr, so use /proc/self/
	// instead of /proc/<tid>/ like libapparmor does
	path := fmt.Sprintf("/proc/self/attr/%s", attr)

	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = fmt.Fprintf(f, "%s", value)
	return err
}

// changeOnExec reimplements aa_change_onexec from libapparmor in Go
func changeOnExec(name string) error {
	value := "exec " + name
	if err := setprocattr("exec", value); err != nil {
		return fmt.Errorf("apparmor failed to apply profile: %s", err)
	}
	return nil
}

func queuedChangeOnExec(name string) bool {
	data, err := ioutil.ReadFile("/proc/self/attr/exec")
	if err != nil {
		return false
	}
	want := "exec " + name
	got := strings.TrimSpace(string(data))
	return got == want || strings.HasPrefix(got, want)
}

// ApplyProfile queues the named profile for the next exec (change_onexec).
func ApplyProfile(name string) error {
	if name == "" {
		return nil
	}
	if err := changeOnExec(name); err != nil {
		// nsexec queues change_onexec before NEWUSER; this write is EPERM
		// afterwards but the next exec still uses the queued profile.
		if queuedChangeOnExec(name) {
			return nil
		}
		return err
	}
	return nil
}
