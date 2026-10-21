package orphan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShouldReapOnlyChildlessUntrackedOrphans(t *testing.T) {
	orphan := InitProc{PID: 10, PPID: 1, JobID: "job-a"}
	if !ShouldReap(orphan, false) {
		t.Fatal("childless untracked containerinit reparented to init must be reaped")
	}
	if ShouldReap(orphan, true) {
		t.Fatal("a containerinit the host still tracks must be left alone")
	}
	withChild := orphan
	withChild.Children = 1
	if ShouldReap(withChild, false) {
		t.Fatal("a containerinit with a workload must be left alone")
	}
	hosted := orphan
	hosted.PPID = 50
	if ShouldReap(hosted, false) {
		t.Fatal("a containerinit still parented by flynn-host must be left alone")
	}
	if ShouldReap(InitProc{PID: 1, PPID: 0, JobID: "job-a"}, false) {
		t.Fatal("pid 1 must never be signaled")
	}
}

func TestReadInitsAndReap(t *testing.T) {
	root := t.TempDir()
	writeProc(t, root, 100, 1, "/.containerinit\x00job-a\x00")
	writeProc(t, root, 200, 1, "/.containerinit\x00job-b\x00")
	writeProc(t, root, 201, 200, "app\x00")
	writeProc(t, root, 300, 50, "/.containerinit\x00job-c\x00")
	writeProc(t, root, 400, 1, "/.containerinit\x00job-d\x00")

	var killed []int
	kill := func(pid int) error {
		killed = append(killed, pid)
		return nil
	}
	tracked := map[int]struct{}{400: {}}
	got, inUse, err := ReapAll(root, tracked, kill)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != 100 {
		t.Fatalf("killed %v", got)
	}
	if len(inUse) != 1 || inUse[0].JobID != "job-b" || inUse[0].PID != 200 {
		t.Fatalf("in use %+v", inUse)
	}
	if len(killed) != 1 || killed[0] != 100 {
		t.Fatalf("kill calls %v", killed)
	}

	killed = nil
	ok, err := ReapJob(root, "job-b", nil, kill)
	if ok {
		t.Fatal("in-use job must not be killed")
	}
	var inUseErr *InUseError
	if err == nil || !errorAs(err, &inUseErr) || inUseErr.PID != 200 {
		t.Fatalf("err %v", err)
	}
	ok, err = ReapJob(root, "job-a", nil, kill)
	if err != nil || !ok || len(killed) != 1 || killed[0] != 100 {
		t.Fatalf("reap job-a ok=%v err=%v killed=%v", ok, err, killed)
	}
}

func errorAs(err error, target **InUseError) bool {
	e, ok := err.(*InUseError)
	if ok {
		*target = e
	}
	return ok
}

func writeProc(t *testing.T, root string, pid, ppid int, cmdline string) {
	t.Helper()
	dir := filepath.Join(root, itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := itoa(pid) + " (name) S " + itoa(ppid) + " 0 0\n"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
