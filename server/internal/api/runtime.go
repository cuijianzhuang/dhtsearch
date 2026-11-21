package api

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// RuntimeStatus is the serving process's own memory footprint,
// published in /api/stats and the admin overview so a long-running
// deployment's memory behaviour is observable without shell access:
// whether RSS plateaus after warm-up or keeps climbing is the
// difference between a healthy process and a leak, and it can only
// be told from a time series of exactly these gauges.
//
// HeapAlloc/HeapSys are the Go runtime's live-heap and heap-obtained
// figures (HeapAlloc sawtooths between GCs; compare post-GC values
// or HeapSys for the trend). RSS is the process resident set from
// /proc/self/statm on Linux — the number the OOM killer sees — and
// 0 where it cannot be read (non-Linux, restricted /proc), in which
// case the heap figures still serve. Goroutines catches the other
// classic growth mode: a leaking goroutine per request or fetch.
type RuntimeStatus struct {
	HeapAlloc  uint64 `json:"heap_alloc"`
	HeapSys    uint64 `json:"heap_sys"`
	RSS        uint64 `json:"rss"`
	Goroutines int    `json:"goroutines"`
}

// runtimeStatus samples the current process. It is cheap (one
// ReadMemStats, one small /proc read) and both callers already cache
// or poll on a multi-second cadence.
func runtimeStatus() RuntimeStatus {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return RuntimeStatus{
		HeapAlloc:  ms.HeapAlloc,
		HeapSys:    ms.HeapSys,
		RSS:        processRSS(),
		Goroutines: runtime.NumGoroutine(),
	}
}

// processRSS returns the process's resident set size in bytes from
// /proc/self/statm, or 0 when unavailable.
func processRSS() uint64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}
