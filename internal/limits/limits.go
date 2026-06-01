package limits

import "github.com/thesouldev/goboxd/internal/config"

// RequestOverride holds optional per-request limit overrides.
// Nil pointer fields mean "use the language default".
type RequestOverride struct {
	WallTimeS    *int
	MemoryKB     *int
	MaxProcesses *int
}

// Merge returns effective limits for a step. The spec defines request limits as
// a partial override: each present field replaces the language default; absent
// fields fall back to the default. There is no server-side ceiling/clamp — a
// request may raise its own budget above the language default. A non-positive
// value is ignored (treated as absent) since zero/negative limits are nonsensical.
func Merge(defaults config.Limits, override *RequestOverride) config.Limits {
	out := defaults
	if override == nil {
		return out
	}
	if override.WallTimeS != nil && *override.WallTimeS > 0 {
		out.WallTimeS = *override.WallTimeS
	}
	if override.MemoryKB != nil && *override.MemoryKB > 0 {
		out.MemoryKB = *override.MemoryKB
	}
	if override.MaxProcesses != nil && *override.MaxProcesses > 0 {
		out.MaxProcesses = *override.MaxProcesses
	}
	return out
}
