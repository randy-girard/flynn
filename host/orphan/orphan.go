// Package orphan reaps containerinit processes that survived a flynn-host
// restart. Those processes keep the job's ext2 zvol mounted, so ZFS reports
// "dataset is busy" and volume GC cannot delete it.
package orphan

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ProcRoot is the procfs used to find containerinit processes.
var ProcRoot = "/proc"

// InitProc is one /.containerinit process visible on the host.
type InitProc struct {
	PID      int
	PPID     int
	Children int
	JobID    string
}

// InUseError means a container still has a workload on this ext2 volume.
type InUseError struct {
	VolumeID string
	PID      int
}

func (e *InUseError) Error() string {
	return fmt.Sprintf("ext2 volume %s is in use by container pid %d", e.VolumeID, e.PID)
}

func pidTracked(tracked map[int]struct{}, pid int) bool {
	_, ok := tracked[pid]
	return ok
}

// ShouldReap reports whether this containerinit is a leftover mount holder.
// A live job is reparented to init after a host restart too, so a process is
// reaped only when the host does not track it, it has no children, and its
// parent is init.
func ShouldReap(p InitProc, tracked bool) bool {
	if p.JobID == "" || tracked || p.Children > 0 || p.PPID != 1 || p.PID <= 1 {
		return false
	}
	return true
}

// ReadInits lists /.containerinit processes under procRoot.
func ReadInits(procRoot string) ([]InitProc, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	type raw struct {
		pid  int
		ppid int
		job  string
	}
	var rows []raw
	ppidCount := map[int]int{}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(ent.Name())
		if err != nil || pid <= 0 {
			continue
		}
		stat, err := os.ReadFile(filepath.Join(procRoot, ent.Name(), "stat"))
		if err != nil {
			continue
		}
		ppid, err := parsePPID(stat)
		if err != nil {
			continue
		}
		ppidCount[ppid]++
		cmd, err := os.ReadFile(filepath.Join(procRoot, ent.Name(), "cmdline"))
		if err != nil {
			continue
		}
		job, ok := parseContainerInitCmd(cmd)
		if !ok {
			continue
		}
		rows = append(rows, raw{pid: pid, ppid: ppid, job: job})
	}
	out := make([]InitProc, 0, len(rows))
	for _, row := range rows {
		out = append(out, InitProc{
			PID:      row.pid,
			PPID:     row.ppid,
			Children: ppidCount[row.pid],
			JobID:    row.job,
		})
	}
	return out, nil
}

func parseContainerInitCmd(cmd []byte) (string, bool) {
	parts := strings.Split(strings.TrimRight(string(cmd), "\x00"), "\x00")
	if len(parts) < 2 || parts[0] != "/.containerinit" {
		return "", false
	}
	job := strings.TrimSpace(parts[1])
	if job == "" {
		return "", false
	}
	return job, true
}

func parsePPID(stat []byte) (int, error) {
	s := string(stat)
	i := strings.LastIndex(s, ")")
	if i < 0 || i+2 >= len(s) {
		return 0, fmt.Errorf("bad stat")
	}
	fields := strings.Fields(s[i+2:])
	if len(fields) < 2 {
		return 0, fmt.Errorf("bad stat")
	}
	return strconv.Atoi(fields[1])
}

// ReapJob kills an untracked, childless containerinit for jobID so its ext2
// zvol can be destroyed. A container that still has a child is left alone.
func ReapJob(procRoot, jobID string, tracked map[int]struct{}, kill func(int) error) (bool, error) {
	if strings.TrimSpace(jobID) == "" {
		return false, nil
	}
	procs, err := ReadInits(procRoot)
	if err != nil {
		return false, err
	}
	var inUse *InUseError
	killed := false
	for _, p := range procs {
		if p.JobID != jobID {
			continue
		}
		if ShouldReap(p, pidTracked(tracked, p.PID)) {
			if err := kill(p.PID); err != nil {
				return killed, err
			}
			killed = true
			continue
		}
		if p.Children > 0 && !pidTracked(tracked, p.PID) {
			inUse = &InUseError{VolumeID: jobID, PID: p.PID}
		}
	}
	if inUse != nil {
		return killed, inUse
	}
	return killed, nil
}

// ReapAll kills every leftover containerinit. Containers that still have a
// workload are returned and not signaled.
func ReapAll(procRoot string, tracked map[int]struct{}, kill func(int) error) (killed []int, inUse []InitProc, err error) {
	procs, err := ReadInits(procRoot)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range procs {
		if ShouldReap(p, pidTracked(tracked, p.PID)) {
			if err := kill(p.PID); err != nil {
				return killed, inUse, err
			}
			killed = append(killed, p.PID)
			continue
		}
		if p.JobID != "" && p.Children > 0 && !pidTracked(tracked, p.PID) {
			inUse = append(inUse, p)
		}
	}
	return killed, inUse, nil
}

// Kill sends SIGKILL and waits until the process is gone.
func Kill(pid int) error {
	if pid <= 1 {
		return fmt.Errorf("refusing to signal pid %d", pid)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		if err == syscall.ESRCH {
			return nil
		}
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("containerinit pid %d did not exit", pid)
}
