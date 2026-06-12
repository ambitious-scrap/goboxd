package status_test

import (
	"strings"
	"testing"

	"github.com/thesouldev/goboxd/internal/status"
)

// FuzzCompareOutput checks the verdict classifier never panics on arbitrary
// bytes and always returns one of the three legal comparison verdicts, with
// exact byte-equality always classified as accepted.
func FuzzCompareOutput(f *testing.F) {
	f.Add("hello\n", "hello\n")
	f.Add("  hi  ", "hi")
	f.Add("a  b", "a b")
	f.Add("", "")

	f.Fuzz(func(t *testing.T, got, expected string) {
		v := status.CompareOutput(got, expected)
		switch v {
		case status.Accepted, status.OutputWhitespaceMismatch, status.WrongOutput:
		default:
			t.Fatalf("illegal verdict %q for got=%q expected=%q", v, got, expected)
		}
		if got == expected && v != status.Accepted {
			t.Fatalf("byte-equal inputs must be accepted, got %q", v)
		}
	})
}

// FuzzTopLevel checks the top-level roll-up: a failed build always wins, and
// otherwise the result is accepted or the first non-accepted test status.
func FuzzTopLevel(f *testing.F) {
	f.Add("ok", "accepted\x1faccepted")
	f.Add("failed", "accepted")
	f.Add("ok", "accepted\x1fwrong_output\x1ftime_exceeded")
	f.Add("ok", "")

	f.Fuzz(func(t *testing.T, buildStatus, joined string) {
		var testStatuses []string
		if joined != "" {
			testStatuses = strings.Split(joined, "\x1f")
		}

		got := status.TopLevel(buildStatus, testStatuses)

		if buildStatus == status.BuildFailed {
			if got != status.TopBuildFailed {
				t.Fatalf("build failed must yield %q, got %q", status.TopBuildFailed, got)
			}
			return
		}

		// Expected: first non-accepted test status, else accepted.
		want := status.Accepted
		for _, s := range testStatuses {
			if s != status.Accepted {
				want = s
				break
			}
		}
		if got != want {
			t.Fatalf("TopLevel(%q,%v)=%q want %q", buildStatus, testStatuses, got, want)
		}
	})
}
