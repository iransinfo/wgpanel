package main

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// cpuSample is a single /proc/stat "cpu" line reading. CPU percent is computed from
// the delta between two samples a tick apart, not from one point-in-time read.
type cpuSample struct {
	idle  uint64
	total uint64
}

// readCPUSample reads the aggregate "cpu" line from /proc/stat. ok=false if
// unreadable (non-Linux, or any parse failure) - callers should omit CPU metrics
// entirely rather than report a fabricated value, the same tolerance readPeerCount
// already has for a missing WireGuard interface.
func readCPUSample() (cpuSample, bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuSample{}, false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return cpuSample{}, false
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuSample{}, false
	}

	var total uint64
	var idle uint64
	for i, field := range fields[1:] {
		v, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return cpuSample{}, false
		}
		total += v
		if i == 3 { // /proc/stat's 4th cpu field is "idle"
			idle = v
		}
	}
	return cpuSample{idle: idle, total: total}, true
}

// cpuPercentBetween computes CPU utilization between two samples (curOK must be true
// - prevOK==false is handled by the caller skipping the first tick, since there's no
// prior sample to diff against yet).
func cpuPercentBetween(prev, cur cpuSample) *float32 {
	totalDelta := cur.total - prev.total
	idleDelta := cur.idle - prev.idle
	if totalDelta == 0 {
		return nil
	}
	pct := float32(totalDelta-idleDelta) / float32(totalDelta) * 100
	return &pct
}

// readMemInfo reads MemTotal/MemAvailable from /proc/meminfo (kB, per the kernel's
// documented format) and returns used/total in bytes. ok=false if unreadable.
func readMemInfo() (usedBytes, totalBytes int64, ok bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()

	var totalKB, availKB int64
	var haveTotal, haveAvail bool
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "MemTotal":
			if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				totalKB, haveTotal = v, true
			}
		case "MemAvailable":
			if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				availKB, haveAvail = v, true
			}
		}
		if haveTotal && haveAvail {
			break
		}
	}
	if !haveTotal || !haveAvail {
		return 0, 0, false
	}
	return (totalKB - availKB) * 1024, totalKB * 1024, true
}
