package web

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// hostStats holds host-level and panel process resource usage.
type hostStats struct {
	// Host
	CPUPercent float64
	NumCPU     int
	CorePcts   []float64 // per-core CPU percentages
	MemUsedMB  float64
	MemTotalMB float64
	// Panel process
	PanelMemMB float64
}

// cpuSample stores idle and total jiffies for one CPU line.
type cpuSample struct {
	idle, total uint64
}

// sysSampler collects host CPU stats using /proc/stat deltas.
type sysSampler struct {
	mu       sync.Mutex
	prevAll  cpuSample   // aggregate "cpu" line
	prevCore []cpuSample // per-core "cpuN" lines
}

func (s *sysSampler) sample() hostStats {
	s.mu.Lock()
	defer s.mu.Unlock()

	var hs hostStats

	// Host CPU from /proc/stat (aggregate + per-core)
	if all, cores, err := readProcStatAll(); err == nil {
		if s.prevAll.total > 0 {
			hs.CPUPercent = cpuDeltaPct(s.prevAll, all)
		}
		s.prevAll = all

		hs.CorePcts = make([]float64, len(cores))
		for i, c := range cores {
			if i < len(s.prevCore) {
				hs.CorePcts[i] = cpuDeltaPct(s.prevCore[i], c)
			}
		}
		s.prevCore = cores
	}

	// Host memory from /proc/meminfo
	if used, total, err := readProcMeminfo(); err == nil {
		hs.MemUsedMB = used
		hs.MemTotalMB = total
	}

	hs.NumCPU = runtime.NumCPU()

	// Panel process memory from runtime
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	hs.PanelMemMB = float64(m.Sys) / (1024 * 1024)

	return hs
}

func cpuDeltaPct(prev, cur cpuSample) float64 {
	totalDelta := cur.total - prev.total
	idleDelta := cur.idle - prev.idle
	if totalDelta == 0 {
		return 0
	}
	return float64(totalDelta-idleDelta) / float64(totalDelta) * 100.0
}

// parseCPULine parses a "cpu" or "cpuN" line from /proc/stat into a cpuSample.
func parseCPULine(line string) cpuSample {
	fields := strings.Fields(line)
	var s cpuSample
	for i, f := range fields[1:] {
		v, _ := strconv.ParseUint(f, 10, 64)
		s.total += v
		if i == 3 || i == 4 { // idle + iowait
			s.idle += v
		}
	}
	return s
}

// readProcStatAll parses /proc/stat and returns the aggregate CPU sample
// plus a per-core slice (cpu0, cpu1, ...).
func readProcStatAll() (cpuSample, []cpuSample, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuSample{}, nil, err
	}
	var all cpuSample
	var cores []cpuSample
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "cpu ") {
			all = parseCPULine(line)
		} else if strings.HasPrefix(line, "cpu") && len(line) > 3 && line[3] >= '0' && line[3] <= '9' {
			cores = append(cores, parseCPULine(line))
		}
	}
	if all.total == 0 {
		return cpuSample{}, nil, fmt.Errorf("no aggregate cpu line found")
	}
	return all, cores, nil
}

// readProcMeminfo returns (usedMB, totalMB) from /proc/meminfo.
func readProcMeminfo() (float64, float64, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	var totalKB, availKB uint64
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fmt.Sscanf(line, "MemTotal: %d kB", &totalKB)
		} else if strings.HasPrefix(line, "MemAvailable:") {
			fmt.Sscanf(line, "MemAvailable: %d kB", &availKB)
		}
	}
	totalMB := float64(totalKB) / 1024
	usedMB := float64(totalKB-availKB) / 1024
	return usedMB, totalMB, nil
}
