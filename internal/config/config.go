package config

type Config struct {
	Server    ServerConfig `yaml:"server"`
	Languages []Language   `yaml:"languages"`
}

type ServerConfig struct {
	Port           int    `yaml:"port"`
	MaxConcurrency int    `yaml:"max_concurrency"`
	MaxBodyBytes   int    `yaml:"max_body_bytes"`
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
}

type SmokeProbe struct {
	Cmd  string   `yaml:"cmd"`
	Args []string `yaml:"args"`
}
