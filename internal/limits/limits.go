package limits

import "github.com/ambitious-scrap/goboxd/internal/config"

// RequestOverride holds optional per-request limit overrides.
// Nil pointer fields mean "use the language default".
type RequestOverride struct {
	WallTimeS    *int
	MemoryKB     *int
	MaxProcesses *int
}

// Merge returns effective limits for a run step, applying any request
// overrides clamped to the language-configured maximums.
func Merge(defaults config.Limits, override *RequestOverride) config.Limits {
	out := defaults
	if override == nil {
		return out
	}
	if override.WallTimeS != nil {
		v := *override.WallTimeS
		if v > 0 && v <= defaults.WallTimeS {
			out.WallTimeS = v
		}
	}
	if override.MemoryKB != nil {
		v := *override.MemoryKB
		if v > 0 && v <= defaults.MemoryKB {
			out.MemoryKB = v
		}
	}
	if override.MaxProcesses != nil {
		v := *override.MaxProcesses
		if v > 0 && v <= defaults.MaxProcesses {
			out.MaxProcesses = v
		}
	}
	return out
}
