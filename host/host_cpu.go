package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"

	host "github.com/randy-girard/flynn/host/types"
)

// procStatPath is the aggregate CPU line source. Tests point it at a fixture.
var procStatPath = "/proc/stat"

// hostCPUTimes is the summed non-guest CPU ticks from the first "cpu " line.
// idle includes iowait. guest and guest_nice are already inside user and nice.
type hostCPUTimes struct {
	idle  uint64
	total uint64
}

func parseHostCPUStat(data []byte) (hostCPUTimes, error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		// cpu user nice system idle iowait irq softirq steal [guest guest_nice]
		if len(fields) < 5 {
			return hostCPUTimes{}, fmt.Errorf("short /proc/stat cpu line")
		}
		nums := make([]uint64, 0, 8)
		for _, field := range fields[1:] {
			if len(nums) == 8 {
				break
			}
			n, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				return hostCPUTimes{}, fmt.Errorf("parse /proc/stat cpu: %w", err)
			}
			nums = append(nums, n)
		}
		var total uint64
		for _, n := range nums {
			total += n
		}
		idle := nums[3]
		if len(nums) > 4 {
			idle += nums[4]
		}
		return hostCPUTimes{idle: idle, total: total}, nil
	}
	if err := sc.Err(); err != nil {
		return hostCPUTimes{}, err
	}
	return hostCPUTimes{}, fmt.Errorf("no cpu line in /proc/stat")
}

func hostCPUPercent(prev, curr hostCPUTimes) float64 {
	if curr.total < prev.total || curr.idle < prev.idle {
		return 0
	}
	totald := curr.total - prev.total
	if totald == 0 {
		return 0
	}
	idled := curr.idle - prev.idle
	if idled > totald {
		return 0
	}
	return float64(totald-idled) / float64(totald) * 100
}

func (l *LibcontainerBackend) readHostCPU(stats *host.HostResourceStats) error {
	data, err := os.ReadFile(procStatPath)
	if err != nil {
		return err
	}
	curr, err := parseHostCPUStat(data)
	if err != nil {
		return err
	}
	l.hostCPUMtx.Lock()
	defer l.hostCPUMtx.Unlock()
	if l.hostCPUHave {
		stats.CPUUsagePercent = hostCPUPercent(l.hostCPUPrev, curr)
	}
	l.hostCPUPrev = curr
	l.hostCPUHave = true
	return nil
}
