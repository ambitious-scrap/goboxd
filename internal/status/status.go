package status

import (
	"strings"
)

const (
	Accepted                 = "accepted"
	WrongOutput              = "wrong_output"
	OutputWhitespaceMismatch = "output_whitespace_mismatch"
	TimeExceeded             = "time_exceeded"
	MemoryExceeded           = "memory_exceeded"
	RuntimeError             = "runtime_error"
	InternalError            = "internal_error"
	NotExecuted              = "not_executed"

	// Build-scoped statuses.
	BuildOK     = "ok"
	BuildFailed = "failed" // build.status value when compilation fails

	// Top-level status when build fails.
	TopBuildFailed = "build_failed"
)

// CompareOutput compares actual output against expected, returning the
// appropriate status string. Whitespace-only differences get their own status.
func CompareOutput(got, expected string) string {
	if got == expected {
		return Accepted
	}
	if normalizeWhitespace(got) == normalizeWhitespace(expected) {
		return OutputWhitespaceMismatch
	}
	return WrongOutput
}

// TopLevel computes the top-level run status from build + per-test statuses.
func TopLevel(buildStatus string, testStatuses []string) string {
	if buildStatus == BuildFailed {
		return TopBuildFailed
	}
	for _, s := range testStatuses {
		if s != Accepted {
			return s
		}
	}
	return Accepted
}

func normalizeWhitespace(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	// trim trailing empty lines
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}
