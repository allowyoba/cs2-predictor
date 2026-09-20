package app

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// What the machine itself is doing.
//
// Everything else this bot measures is about its own work — jobs, queues,
// providers. None of it notices the failure that takes all of them down at
// once: the disk filling up, or memory running out. Those arrive as
// unrelated-looking errors everywhere and are obvious only afterwards.
//
// Read from the kernel directly rather than through a dependency: three
// files and one syscall, all of them stable interfaces, against a library
// that would have to be kept current for the same numbers.

// HostUsage is one reading of the machine's own resources. Fractions are
// 0..1 so a threshold reads as a percentage without unit confusion.
type HostUsage struct {
	MemoryUsed float64
	DiskUsed   float64
	// LoadPerCPU is the one-minute load average divided by the number of
	// CPUs, which is the form that means the same thing on every machine.
	LoadPerCPU float64
}

// HostLimits are the levels at which somebody has to do something.
//
// Chosen to fire while there is still room to act rather than at the
// moment it stops working: a disk reported at 99% has usually been broken
// for hours, and the alert everybody remembers is the one that came too
// late to matter.
type HostLimits struct {
	Memory float64
	Disk   float64
	Load   float64
}

// DefaultHostLimits is what the bot runs with unless told otherwise.
func DefaultHostLimits() HostLimits {
	return HostLimits{Memory: 0.90, Disk: 0.85, Load: 4.0}
}

// HostMonitor samples the machine and alerts when a limit is crossed.
//
// One alert per resource per crossing, with a matching recovery — the same
// shape the provider and webhook watchers use, and for the same reason:
// an alert repeated every few minutes is an alert people filter out.
type HostMonitor struct {
	Metrics *Metrics
	Alerter *AdminAlerter
	Limits  HostLimits
	Log     *slog.Logger
	// Root is the filesystem whose free space matters — the one the
	// database and the images live on.
	Root string

	breached map[string]bool
}

// Check samples once and reports what changed.
func (m *HostMonitor) Check(ctx context.Context) {
	usage, err := ReadHostUsage(m.Root)
	if err != nil {
		m.Log.Warn("host usage unavailable", "error", err)
		return
	}
	if m.Metrics != nil {
		m.Metrics.HostUsage.WithLabelValues("memory").Set(usage.MemoryUsed)
		m.Metrics.HostUsage.WithLabelValues("disk").Set(usage.DiskUsed)
		m.Metrics.HostUsage.WithLabelValues("load_per_cpu").Set(usage.LoadPerCPU)
	}
	m.evaluate(ctx, "memory", usage.MemoryUsed, m.Limits.Memory)
	m.evaluate(ctx, "disk", usage.DiskUsed, m.Limits.Disk)
	m.evaluate(ctx, "load", usage.LoadPerCPU, m.Limits.Load)
}

// evaluate alerts on the way past a limit and again on the way back.
func (m *HostMonitor) evaluate(ctx context.Context, resource string, value, limit float64) {
	if limit <= 0 {
		return
	}
	if m.breached == nil {
		m.breached = map[string]bool{}
	}
	over := value >= limit
	if over == m.breached[resource] {
		return
	}
	m.breached[resource] = over
	if over {
		m.Log.Error("host resource is running out", "resource", resource, "value", value, "limit", limit)
		if m.Alerter != nil {
			m.Alerter.HostPressure(ctx, resource, describeUsage(resource, value), describeUsage(resource, limit))
		}
		return
	}
	m.Log.Info("host resource recovered", "resource", resource, "value", value)
	if m.Alerter != nil {
		m.Alerter.HostRecovered(ctx, resource, describeUsage(resource, value))
	}
}

// describeUsage renders a reading the way the resource is normally spoken
// about: a percentage for the two that are one, a bare number for load.
func describeUsage(resource string, value float64) string {
	if resource == "load" {
		return strconv.FormatFloat(value, 'f', 2, 64)
	}
	return strconv.Itoa(int(value*100+0.5)) + "%"
}

// ReadHostUsage samples memory, disk and load. Linux-specific by design:
// this runs in a container on a Linux VM, and a portable abstraction over
// numbers that only exist there would be pretending.
func ReadHostUsage(root string) (HostUsage, error) {
	if root == "" {
		root = "/"
	}
	memory, err := readMemoryUsed()
	if err != nil {
		return HostUsage{}, err
	}
	disk, err := readDiskUsed(root)
	if err != nil {
		return HostUsage{}, err
	}
	load, err := readLoadPerCPU()
	if err != nil {
		return HostUsage{}, err
	}
	return HostUsage{MemoryUsed: memory, DiskUsed: disk, LoadPerCPU: load}, nil
}

// readMemoryUsed uses MemAvailable, which is the kernel's own estimate of
// what a new allocation could actually get — unlike MemFree, which counts
// cache as used and reports a healthy machine as nearly full.
func readMemoryUsed() (float64, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()

	var total, available float64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = value
		case "MemAvailable:":
			available = value
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	if total <= 0 {
		return 0, fmt.Errorf("meminfo reported no total memory")
	}
	return 1 - available/total, nil
}

func readDiskUsed(root string) (float64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(root, &stat); err != nil {
		return 0, err
	}
	total := float64(stat.Blocks) * float64(stat.Bsize)
	if total <= 0 {
		return 0, fmt.Errorf("statfs reported no blocks for %s", root)
	}
	// Available, not free: the reserve root-only blocks are not space this
	// process can use, and counting them is how a disk looks fine until
	// the moment it does not.
	available := float64(stat.Bavail) * float64(stat.Bsize)
	return 1 - available/total, nil
}

func readLoadPerCPU() (float64, error) {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, fmt.Errorf("loadavg was empty")
	}
	load, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, err
	}
	cpus := float64(runtime.NumCPU())
	if cpus <= 0 {
		cpus = 1
	}
	return load / cpus, nil
}
