package config

type Config struct {
	Server    ServerConfig `yaml:"server"`
	Languages []Language   `yaml:"languages"`
}

type ServerConfig struct {
	Port           int    `yaml:"port"`
	MaxConcurrency int    `yaml:"max_concurrency"`
	MaxBodyBytes   int    `yaml:"max_body_bytes"`
	MaxSourceBytes int    `yaml:"max_source_bytes"`
	JailBase       string `yaml:"jail_base"`
	NsjailPath     string `yaml:"nsjail_path"`
	OutputCapBytes int    `yaml:"output_cap_bytes"`
	MaxTests       int    `yaml:"max_tests"`
	// SeccompMode controls nsjail seccomp-bpf filtering, applied to every run:
	//   "off"     — no syscall filter (default; current behaviour, no regression)
	//   "audit"   — load the per-language policy AND pass --seccomp_log, so
	//               violations are logged (author the policy with a permissive
	//               default action to observe without killing). Audit-first.
	//   "enforce" — load the per-language policy as written (DEFAULT KILL etc.)
	// A language with no seccomp_policy is never filtered, regardless of mode.
	SeccompMode string `yaml:"seccomp_mode"`
}

type Language struct {
	ID                       string     `yaml:"id"`
	Name                     string     `yaml:"name"`
	SourceFilename           string     `yaml:"source_filename"`
	Artifact                 string     `yaml:"artifact,omitempty"`
	SourceFilenameStrategy   string     `yaml:"source_filename_strategy,omitempty"`
	ArtifactFilenameStrategy string     `yaml:"artifact_filename_strategy,omitempty"`
	Build                    *BuildStep `yaml:"build,omitempty"`
	Run                      RunStep    `yaml:"run"`
	Smoke                    SmokeProbe `yaml:"smoke"`
	// SeccompPolicy is an optional kafel seccomp-bpf program applied to this
	// language's build and run steps when SeccompMode != "off". Empty = no
	// filter. JIT/interpreted runtimes (Node/V8, the JVM) need mprotect with
	// PROT_EXEC, so per-language policies differ; author accordingly.
	SeccompPolicy string `yaml:"seccomp_policy,omitempty"`
}

type BuildStep struct {
	Cmd           string   `yaml:"cmd"`
	Args          []string `yaml:"args"`
	Limits        Limits   `yaml:"limits"`
	FlagAllowlist []string `yaml:"flag_allowlist"`
}

type RunStep struct {
	Cmd           string   `yaml:"cmd"`
	Args          []string `yaml:"args"`
	Limits        Limits   `yaml:"limits"`
	FlagAllowlist []string `yaml:"flag_allowlist"`
}

type Limits struct {
	WallTimeS    int `yaml:"wall_time_s"`
	MemoryKB     int `yaml:"memory_kb"`
	MaxProcesses int `yaml:"max_processes"`
	// CPUMaxPercent caps CPU bandwidth via the cgroup v2 cpu controller, as a
	// percentage of one core (100 = one full core, 200 = two cores). It is a
	// server-side, per-language control only: it is NOT part of the request
	// limit schema (spec request limits are wall_time_s/memory_kb/max_processes),
	// so a request cannot raise or lower it. 0 means unlimited (cpu.max=max).
	//
	// Note: a sub-core quota (<100) throttles the process and inflates wall-clock
	// time, which can trip the wall_time_s limit. Leave at 0 unless graded on
	// CPU-time rather than wall-time.
	CPUMaxPercent int `yaml:"cpu_max_percent"`
}

type SmokeProbe struct {
	Cmd  string   `yaml:"cmd"`
	Args []string `yaml:"args"`
}
