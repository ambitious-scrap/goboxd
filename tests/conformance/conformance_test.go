//go:build integration

// Package conformance runs a differential test of the goboxd HTTP service
// against the reference implementation's own recorded test vectors
// (pyjail/src/tests/testcases/<lang>/<case>/{request.txt,reply.txt}, protobuf
// text format). For each fixture it drives POST /run and asserts our verdict
// matches the reference reply, across every language we support.
//
// Run against a live service (e.g. the Docker container):
//
//	GOBOXD_URL=http://localhost:8080 go test -tags integration ./tests/conformance/...
//
// The reference fixtures live outside the module under ./pyjail (provided
// locally, gitignored). If they or the service are absent the test skips.
package conformance

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// our registered language ids; reference dirs/ids outside this set are skipped
// (e.g. "python" py2, "zip" multi-file).
var supported = map[string]bool{
	"py3": true, "c": true, "cpp": true, "java": true,
	"bash": true, "javascript": true, "verilog": true,
}

// knownReferenceBugs maps "lang/case" fixtures where the reference reply is
// provably wrong (the reference's own recorded output is incorrect) to the
// verdict goboxd *correctly* returns. We assert our correct verdict rather than
// blindly matching a buggy reference.
//
//   - java/error_runtime: the fixture's own source comment admits the reference
//     "returns OK" for an uncaught ArithmeticException (divide-by-zero, JVM exit
//     code 1) that "should cause RUNTIME_ERROR". goboxd detects the non-zero
//     exit and returns runtime_error, which is correct.
var knownReferenceBugs = map[string]string{
	"java/error_runtime": "runtime_error",
}

// reference CodeReply.Status enum -> acceptable goboxd top-level status(es).
var statusMap = map[string][]string{
	"OK":                    {"accepted"},
	"COMPILATION_ERROR":     {"build_failed"},
	"WRONG_ANSWER":          {"wrong_output", "output_whitespace_mismatch"},
	"TIME_LIMIT_EXCEEDED":   {"time_exceeded"},
	"RUNTIME_ERROR":         {"runtime_error"},
	"MEMORY_LIMIT_EXCEEDED": {"memory_exceeded"},
}

func baseURL() string {
	if u := os.Getenv("GOBOXD_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://localhost:8080"
}

func fixturesRoot() string {
	if r := os.Getenv("GOBOXD_FIXTURES"); r != "" {
		return r
	}
	// repo-root/pyjail/src/tests/testcases relative to this file's package dir.
	return filepath.FromSlash("../../pyjail/src/tests/testcases")
}

func TestDifferentialConformance(t *testing.T) {
	root := fixturesRoot()
	if _, err := os.Stat(root); err != nil {
		t.Skipf("reference fixtures not found at %s (provide pyjail/ locally): %v", root, err)
	}
	requireService(t)

	cases := collectFixtures(t, root)
	if len(cases) == 0 {
		t.Skip("no fixtures matched supported languages")
	}

	for _, fc := range cases {
		t.Run(fc.name, func(t *testing.T) {
			got := runSubmission(t, fc)

			// Where the reference is known-buggy, assert our correct verdict.
			if correct, bug := knownReferenceBugs[fc.name]; bug {
				if got != correct {
					t.Errorf("language=%s case=%s: goboxd status=%q, want corrected %q (reference is buggy: returns %q)",
						fc.language, fc.name, got, correct, fc.replyStatus)
				} else {
					t.Logf("reference-bug case %s: goboxd correctly returns %q (reference wrongly records %q)",
						fc.name, got, fc.replyStatus)
				}
				return
			}

			want, ok := statusMap[fc.replyStatus]
			if !ok {
				t.Skipf("unmapped reference status %q", fc.replyStatus)
			}
			if !contains(want, got) {
				t.Errorf("language=%s case=%s: goboxd status=%q, reference=%q (accept %v)",
					fc.language, fc.name, got, fc.replyStatus, want)
			}
		})
	}
}

// ---- fixture model ---------------------------------------------------------

type fixture struct {
	name        string // lang/case
	language    string
	source      string
	srcFilename string // from code.filename (java etc.); "" otherwise
	binFilename string // from code.binary_filename
	extraArgs   []string
	tests       []testCase
	replyStatus string // reply overall_status
}

type testCase struct {
	stdin    string
	expected string
}

func collectFixtures(t *testing.T, root string) []fixture {
	var out []fixture
	langDirs, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read fixtures root: %v", err)
	}
	for _, ld := range langDirs {
		if !ld.IsDir() {
			continue
		}
		caseDirs, err := os.ReadDir(filepath.Join(root, ld.Name()))
		if err != nil {
			continue
		}
		for _, cd := range caseDirs {
			if !cd.IsDir() {
				continue
			}
			dir := filepath.Join(root, ld.Name(), cd.Name())
			reqBytes, err1 := os.ReadFile(filepath.Join(dir, "request.txt"))
			repBytes, err2 := os.ReadFile(filepath.Join(dir, "reply.txt"))
			if err1 != nil || err2 != nil {
				continue
			}
			f := parseRequest(string(reqBytes))
			f.replyStatus = parseOverallStatus(string(repBytes))
			f.name = ld.Name() + "/" + cd.Name()
			if !supported[f.language] || len(f.tests) == 0 || f.replyStatus == "" {
				continue
			}
			out = append(out, f)
		}
	}
	return out
}

// ---- minimal protobuf-text parser (only the fields we need) ----------------

func parseRequest(s string) fixture {
	var f fixture
	var cur *testCase
	depthTestcase := false
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "language:"):
			f.language = unquote(after(line, "language:"))
		case strings.HasPrefix(line, "full_code:"):
			f.source = unquote(after(line, "full_code:"))
		case strings.HasPrefix(line, "filename:"):
			f.srcFilename = unquote(after(line, "filename:"))
		case strings.HasPrefix(line, "binary_filename:"):
			f.binFilename = unquote(after(line, "binary_filename:"))
		case strings.HasPrefix(line, "extra_args:"):
			f.extraArgs = append(f.extraArgs, unquote(after(line, "extra_args:")))
		case strings.HasPrefix(line, "testcases {"):
			depthTestcase = true
			f.tests = append(f.tests, testCase{})
			cur = &f.tests[len(f.tests)-1]
		case line == "}":
			depthTestcase = false
			cur = nil
		case depthTestcase && cur != nil && strings.HasPrefix(line, "input:"):
			cur.stdin = unquote(after(line, "input:"))
		case depthTestcase && cur != nil && strings.HasPrefix(line, "output:"):
			cur.expected = unquote(after(line, "output:"))
		}
	}
	return f
}

func parseOverallStatus(s string) string {
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "overall_status:") {
			return strings.TrimSpace(after(line, "overall_status:"))
		}
	}
	return ""
}

func after(line, key string) string { return strings.TrimSpace(strings.TrimPrefix(line, key)) }

// unquote turns a protobuf-text quoted string into its bytes. proto-text uses
// the same escapes as Go for our fixtures (\n \t \r \" \\ \xNN, octal), so
// strconv.Unquote handles them; fall back to a manual pass otherwise.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '"' {
		return s
	}
	if v, err := strconv.Unquote(s); err == nil {
		return v
	}
	return manualUnquote(s[1 : len(s)-1])
}

func manualUnquote(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '"', '\\', '\'':
			b.WriteByte(s[i])
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// ---- HTTP driver -----------------------------------------------------------

func runSubmission(t *testing.T, f fixture) string {
	body := map[string]any{
		"language": f.language,
		"source":   f.source,
	}
	if f.srcFilename != "" {
		body["source_filename"] = f.srcFilename
	}
	if f.binFilename != "" {
		body["artifact_filename"] = f.binFilename
	}
	if len(f.extraArgs) > 0 {
		body["build"] = map[string]any{"flags": f.extraArgs}
	}
	tests := make([]map[string]any, 0, len(f.tests))
	for _, tc := range f.tests {
		tests = append(tests, map[string]any{"stdin": tc.stdin, "expected_stdout": tc.expected})
	}
	body["tests"] = tests

	buf, _ := json.Marshal(body)
	resp, err := http.Post(baseURL()+"/run", "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("POST /run: %v", err)
	}
	defer resp.Body.Close()

	var r struct {
		Status string `json:"status"`
		Error  struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode response (HTTP %d): %v", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d code=%s msg=%s", resp.StatusCode, r.Error.Code, r.Error.Message)
	}
	return r.Status
}

func requireService(t *testing.T) {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(baseURL() + "/healthz")
	if err != nil {
		t.Skipf("goboxd not reachable at %s: %v", baseURL(), err)
	}
	resp.Body.Close()
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
