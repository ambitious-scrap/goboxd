package sandbox

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// cgroupRoot is the cgroup v2 unified mount. Inside a container started with
// --cgroupns=host (or a delegated cgroup) this is writable by the service.
const cgroupRoot = "/sys/fs/cgroup"

// cgroupParent is the dedicated subtree goboxd creates its per-run cgroups under.
var cgroupParent = filepath.Join(cgroupRoot, "goboxd")

// cgroup is a per-execution cgroup v2 directory used for memory accounting.
// All operations are best-effort: if the host does not expose a writable
// cgroup v2 tree, ok stays false and the sandbox falls back to rlimit-only
// enforcement with no OOM/peak reporting.
type cgroup struct {
	path string
	ok   bool
}

// cpuQuotaPeriodUs is the cgroup v2 cpu.max accounting period in microseconds.
// cpu.max is written as "<quota_us> <period_us>"; quota = period * percent/100.
const cpuQuotaPeriodUs = 100000

// Controller enablement is computed exactly once (the first request that needs
// it). setup runs under sync.Once because requests are concurrent and the
// evacuation/subtree writes must not race. controllerAvailable records which of
// memory/cpu/pids were successfully delegated into cgroupParent; memory gates
// per-run accounting, cpu/pids are opportunistic hardening.
var (
	memControllerOnce    sync.Once
	memControllerEnabled bool
	controllerAvailable  = map[string]bool{}
)

// setupCgroup creates a dedicated cgroup named id and applies the per-run
// resource caps that the available controllers support: a hard memory cap
// (memoryKB), an absolute process-count cap (maxProcesses via pids.max), and an
// optional CPU bandwidth cap (cpuMaxPercent via cpu.max). Returns a cgroup whose
// ok field reports whether memory accounting is available for this run. pids/cpu
// caps are best-effort: a missing controller is skipped silently.
func setupCgroup(id string, memoryKB, maxProcesses, cpuMaxPercent int) *cgroup {
	cg := &cgroup{}

	if !enableMemoryController() {
		return cg
	}

	path := filepath.Join(cgroupParent, id)
	if err := os.Mkdir(path, 0o755); err != nil {
		return cg
	}

	if memoryKB > 0 {
		limit := strconv.FormatInt(int64(memoryKB)*1024, 10)
		// memory.max enforces the hard limit on this cgroup and its descendants
		// (nsjail creates a child cgroup underneath). memory.swap.max=0 makes the
		// limit count real memory, so a process can't dodge the cap via swap.
		if err := os.WriteFile(filepath.Join(path, "memory.max"), []byte(limit), 0o644); err != nil {
			slog.Warn("cgroup: write memory.max failed; memory cap may not be enforced", "path", path, "err", err)
		}
		if err := os.WriteFile(filepath.Join(path, "memory.swap.max"), []byte("0"), 0o644); err != nil {
			slog.Warn("cgroup: write memory.swap.max failed; swap not disabled", "path", path, "err", err)
		}
	}

	// pids.max is the absolute fork-bomb guard: pids.current is hierarchical, so
	// a cap here bounds the whole subtree (including nsjail's child cgroup). This
	// is stronger than --rlimit_nproc, which is per-UID across the host and so is
	// shared by every concurrent run under the sandbox UID.
	if maxProcesses > 0 && controllerAvailable["pids"] {
		if err := os.WriteFile(filepath.Join(path, "pids.max"), []byte(strconv.Itoa(maxProcesses)), 0o644); err != nil {
			slog.Warn("cgroup: write pids.max failed; process cap relies on rlimit_nproc only", "path", path, "err", err)
		}
	}

	// cpu.max throttles CPU bandwidth for the subtree. Opt-in per language; 0
	// leaves it at the inherited default ("max", unlimited).
	if cpuMaxPercent > 0 && controllerAvailable["cpu"] {
		quota := cpuQuotaPeriodUs * cpuMaxPercent / 100
		val := strconv.Itoa(quota) + " " + strconv.Itoa(cpuQuotaPeriodUs)
		if err := os.WriteFile(filepath.Join(path, "cpu.max"), []byte(val), 0o644); err != nil {
			slog.Warn("cgroup: write cpu.max failed; CPU quota not enforced", "path", path, "err", err)
		}
	}

	cg.path = path
	cg.ok = true
	return cg
}

// MemoryAccountingAvailable reports whether the cgroup v2 memory controller is
// usable for per-run accounting (memory.max + OOM/peak readout). When false the
// sandbox falls back to rlimit address-space enforcement with no OOM detection.
// Idempotent: the underlying probe runs at most once. Call at startup to surface
// the capability in /info and to warm the one-time setup before the first run.
func MemoryAccountingAvailable() bool { return enableMemoryController() }

// enableMemoryController ensures cgroupParent exists and that the memory
// controller is delegated into it so child cgroups can set memory.max.
func enableMemoryController() bool {
	memControllerOnce.Do(func() {
		if _, err := os.Stat(cgroupRoot); err != nil {
			return
		}
		if err := os.MkdirAll(cgroupParent, 0o755); err != nil {
			return
		}
		// In a container the cgroup-namespace root usually holds the service's own
		// processes, and cgroup v2 forbids enabling a controller in a cgroup that
		// has internal processes ("no internal process" rule). The first attempt
		// may therefore fail; if so, evacuate the root's processes into a leaf
		// cgroup and retry. Success is verified via the parent's cgroup.controllers.
		if !tryEnableControllers() {
			evacuateRootProcs()
			if !tryEnableControllers() {
				return
			}
		}
		memControllerEnabled = true
	})
	return memControllerEnabled
}

// tryEnableControllers delegates the memory, cpu and pids controllers into the
// root and parent subtree_control, then records in controllerAvailable which of
// them actually became available in cgroupParent. It reports whether memory (the
// controller that gates per-run accounting) is available; cpu and pids are
// opportunistic and may be absent on hosts that don't delegate them.
func tryEnableControllers() bool {
	// Enable each controller independently: a single subtree_control write is
	// rejected wholesale if any one controller can't be delegated, so batching
	// "+memory +cpu +pids" would let a missing cpu controller block memory too.
	for _, ctrl := range []string{"+memory", "+cpu", "+pids"} {
		_ = os.WriteFile(filepath.Join(cgroupRoot, "cgroup.subtree_control"), []byte(ctrl), 0o644)
		_ = os.WriteFile(filepath.Join(cgroupParent, "cgroup.subtree_control"), []byte(ctrl), 0o644)
	}

	data, err := os.ReadFile(filepath.Join(cgroupParent, "cgroup.controllers"))
	if err != nil {
		return false
	}
	for c := range controllerAvailable {
		delete(controllerAvailable, c)
	}
	for _, c := range strings.Fields(string(data)) {
		controllerAvailable[c] = true
	}
	return controllerAvailable["memory"]
}

// evacuateRootProcs moves every process in the cgroup-namespace root into a
// leaf cgroup, so the root has no internal processes. cgroup v2 requires this
// before a controller can be enabled in the root's subtree_control. Best-effort:
// cgroup.procs accepts one PID per write, and failures are ignored.
func evacuateRootProcs() {
	leaf := filepath.Join(cgroupRoot, "_svc")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		return
	}
	data, err := os.ReadFile(filepath.Join(cgroupRoot, "cgroup.procs"))
	if err != nil {
		return
	}
	dst := filepath.Join(leaf, "cgroup.procs")
	for _, pid := range strings.Fields(string(data)) {
		_ = os.WriteFile(dst, []byte(pid), 0o644)
	}
}

// oomKilled reports whether any process in this cgroup or a descendant was
// OOM-killed. In cgroup v2 the non-local memory.events oom_kill counter is
// recursive, so it captures kills inside the child cgroup nsjail creates.
func (c *cgroup) oomKilled() bool {
	if !c.ok {
		return false
	}
	data, err := os.ReadFile(filepath.Join(c.path, "memory.events"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "oom_kill ") {
			parts := strings.Fields(line)
			if len(parts) == 2 {
				n, _ := strconv.ParseInt(parts[1], 10, 64)
				return n > 0
			}
		}
	}
	return false
}

// memPeakKB returns the peak memory usage of the cgroup subtree in kilobytes.
func (c *cgroup) memPeakKB() int64 {
	if !c.ok {
		return 0
	}
	data, err := os.ReadFile(filepath.Join(c.path, "memory.peak"))
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return n / 1024
}

// cleanup removes the cgroup. nsjail's child cgroup must be removed first; a
// cgroup directory can only be rmdir'd once empty of processes and children.
func (c *cgroup) cleanup() {
	if !c.ok {
		return
	}
	if entries, err := os.ReadDir(c.path); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				_ = os.Remove(filepath.Join(c.path, e.Name()))
			}
		}
	}
	_ = os.Remove(c.path)
}

// SweepOrphanCgroups removes leftover per-run cgroups older than maxAge. Call
// once at startup to clean up after a crashed previous process.
func SweepOrphanCgroups(maxAge time.Duration) { sweepOrphanCgroups(maxAge) }

// sweepOrphanCgroups removes leftover per-run cgroups from a previous process
// that crashed before cleanup. Called once at startup. A cgroup is removed only
// if it has no live processes (rmdir fails otherwise, which is fine).
func sweepOrphanCgroups(maxAge time.Duration) {
	entries, err := os.ReadDir(cgroupParent)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		dir := filepath.Join(cgroupParent, e.Name())
		if sub, err := os.ReadDir(dir); err == nil {
			for _, s := range sub {
				if s.IsDir() {
					_ = os.Remove(filepath.Join(dir, s.Name()))
				}
			}
		}
		_ = os.Remove(dir)
	}
}
