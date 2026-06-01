package status_test

import (
	"testing"

	"github.com/thesouldev/goboxd/internal/status"
)

func TestCompareOutput(t *testing.T) {
	cases := []struct {
		got, expected, want string
	}{
		{"hello\n", "hello\n", status.Accepted},
		{"hello\n", "world\n", status.WrongOutput},
		// trailing whitespace difference
		{"hello  \n", "hello\n", status.OutputWhitespaceMismatch},
		// CRLF vs LF
		{"hello\r\n", "hello\n", status.OutputWhitespaceMismatch},
		// trailing newline difference
		{"hello\n\n", "hello\n", status.OutputWhitespaceMismatch},
		// completely different
		{"foo", "bar", status.WrongOutput},
		// exact empty
		{"", "", status.Accepted},
		// whitespace-only content vs empty
		{"\n", "", status.OutputWhitespaceMismatch},
		// leading/trailing whitespace of the whole output is ignored
		{"  hello\n", "hello", status.OutputWhitespaceMismatch},
		// internal whitespace differences are NOT normalized (matches reference impl)
		{"a  b\n", "a b\n", status.WrongOutput},
	}
	for _, tc := range cases {
		got := status.CompareOutput(tc.got, tc.expected)
		if got != tc.want {
			t.Errorf("CompareOutput(%q, %q) = %q, want %q", tc.got, tc.expected, got, tc.want)
		}
	}
}

func TestTopLevel(t *testing.T) {
	cases := []struct {
		buildStatus string
		tests       []string
		want        string
	}{
		{status.BuildFailed, nil, status.TopBuildFailed},
		{status.BuildFailed, []string{status.NotExecuted}, status.TopBuildFailed},
		{status.BuildOK, []string{status.Accepted, status.Accepted}, status.Accepted},
		{status.BuildOK, []string{status.Accepted, status.WrongOutput}, status.WrongOutput},
		{status.BuildOK, []string{status.TimeExceeded, status.WrongOutput}, status.TimeExceeded},
		{"", []string{status.Accepted}, status.Accepted},
		{"", []string{status.RuntimeError}, status.RuntimeError},
	}
	for _, tc := range cases {
		got := status.TopLevel(tc.buildStatus, tc.tests)
		if got != tc.want {
			t.Errorf("TopLevel(%q, %v) = %q, want %q", tc.buildStatus, tc.tests, got, tc.want)
		}
	}
}
