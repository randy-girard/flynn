package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const oomEventPollInterval = time.Second

// jobCgroupCandidates are the cgroup directories Flynn containers use on
// v1 and v2. libcontainer NotifyOOM looks for memory.oom_control, which
// does not exist on the unified hierarchy.
func jobCgroupCandidates(partition, jobID string) []string {
	if partition == "" {
		partition = "user"
	}
	return []string{
		filepath.Join(cgroupRoot, "flynn", partition, jobID),
		filepath.Join(cgroupRoot, "cgroup", "flynn", partition, jobID),
		filepath.Join(cgroupRoot, "memory", "flynn", partition, jobID),
	}
}

func findExistingDir(candidates []string) string {
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return dir
		}
	}
	return ""
}

func memoryEventsPath(dir string) string {
	return filepath.Join(dir, "memory.events")
}

func memoryOOMControlPath(dir string) string {
	return filepath.Join(dir, "memory.oom_control")
}

// parseOOMKill reads the oom_kill counter from cgroup v2 memory.events.
// Returns ok=false when the file has no oom_kill line.
func parseOOMKill(data []byte) (uint64, bool) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		if fields[0] != "oom_kill" {
			continue
		}
		n, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

func readOOMKill(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	n, ok := parseOOMKill(data)
	if !ok {
		return 0, fmt.Errorf("no oom_kill in %s", path)
	}
	return n, nil
}

// watchMemoryEvents polls cgroup v2 memory.events and sends on the returned
// channel whenever oom_kill increases. stop closes the watcher (container
// exited). The channel is closed when the cgroup disappears or stop fires.
func watchMemoryEvents(eventsPath string, interval time.Duration, stop <-chan struct{}) (<-chan struct{}, error) {
	if interval <= 0 {
		interval = oomEventPollInterval
	}
	last, err := readOOMKill(eventsPath)
	if err != nil {
		return nil, err
	}
	ch := make(chan struct{}, 1)
	go func() {
		defer close(ch)
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				n, err := readOOMKill(eventsPath)
				if err != nil {
					return
				}
				if n > last {
					last = n
					select {
					case ch <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
	return ch, nil
}
