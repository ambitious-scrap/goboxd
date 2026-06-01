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
// appropriate status string. This matches the reference implementation
// (pyjail code_runner.py): exact byte equality is "accepted"; otherwise, if the
// strings are equal after trimming leading/trailing whitespace from the whole
// output, it is a whitespace-only mismatch; otherwise it is wrong output.
// Note: differences in *internal* whitespace (e.g. "a  b" vs "a b") are NOT
// normalized and remain wrong_output, per the reference.
func CompareOutput(got, expected string) string {
	if got == expected {
		return Accepted
	}
	if strings.TrimSpace(got) == strings.TrimSpace(expected) {
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
