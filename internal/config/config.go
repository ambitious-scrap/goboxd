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
