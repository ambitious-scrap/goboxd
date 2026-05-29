package flags

import (
	"fmt"
	"path/filepath"
	"strings"
)

// dangerous prefix patterns always rejected regardless of allowlist
var blockedPrefixes = []string{
	"-fplugin=",
	"--specs=",
	"-Wl,",
	"-x",
	"-B",
}

// Validate checks each requested flag against the per-language allowlist.
// Returns an error describing the first rejected flag, or nil if all pass.
func Validate(requested []string, allowlist []string) error {
	for _, flag := range requested {
		if strings.HasPrefix(flag, "@") {
			return fmt.Errorf("flag %q not allowed: response file flags are prohibited", flag)
		}
		for _, blocked := range blockedPrefixes {
			if strings.HasPrefix(flag, blocked) {
				return fmt.Errorf("flag %q not allowed: prohibited prefix %q", flag, blocked)
			}
		}
		if !matchesAllowlist(flag, allowlist) {
			return fmt.Errorf("flag %q not in allowlist", flag)
		}
	}
	return nil
}

func matchesAllowlist(flag string, allowlist []string) bool {
	for _, pattern := range allowlist {
		if pattern == flag {
			return true
		}
		// filepath.Match supports * wildcards; use for patterns like "-std=*"
		if matched, _ := filepath.Match(pattern, flag); matched {
			return true
		}
	}
	return false
}
