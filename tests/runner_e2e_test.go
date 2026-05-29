package tests_test

import (
	"context"
	"os"
	"testing"

	"github.com/ambitious-scrap/goboxd/internal/config"
	"github.com/ambitious-scrap/goboxd/internal/runner"
	"github.com/ambitious-scrap/goboxd/internal/sandbox"
	"github.com/ambitious-scrap/goboxd/internal/status"
)

// fakeSandbox replays scripted results in order, one per Run call.
type fakeSandbox struct {
	results []*sandbox.Result
	idx     int
}

func (f *fakeSandbox) Run(_ context.Context, _ sandbox.RunConfig) (*sandbox.Result, error) {
	r := f.results[f.idx]
	f.idx++
	return r, nil
}

func ok(stdout string) *sandbox.Result  { return &sandbox.Result{ExitCode: 0, Stdout: stdout} }
func fail(code int) *sandbox.Result     { return &sandbox.Result{ExitCode: code} }
func oom() *sandbox.Result              { return &sandbox.Result{ExitCode: 1, OOMKilled: true} }
func timeout() *sandbox.Result          { return &sandbox.Result{ExitCode: 1, TimedOut: true} }

var py3 = &config.Language{
	ID:             "py3",
	Name:           "Python 3",
	SourceFilename: "solution.py",
	Run: config.RunStep{
		Cmd:  "/usr/bin/python3",
		Args: []string{"{{source}}"},
	},
}

var cpp = &config.Language{
	ID:             "cpp",
	Name:           "C++",
	SourceFilename: "solution.cpp",
	Artifact:       "solution",
	Build: &config.BuildStep{
		Cmd:  "/usr/bin/g++",
		Args: []string{"-o", "{{artifact}}", "{{source}}"},
	},
	Run: config.RunStep{
		Cmd:  "./{{artifact}}",
		Args: []string{},
	},
}

func newRunner(results ...*sandbox.Result) *runner.Runner {
	return runner.NewWithSandbox(&fakeSandbox{results: results}, os.TempDir(), 65536)
}

func TestPy3_Accepted(t *testing.T) {
	r := newRunner(ok("hello\n"))
	res, err := r.Execute(context.Background(), runner.RunRequest{
		Language: py3,
		Source:   `print("hello")`,
		Tests:    []runner.TestCase{{Expected: "hello\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.TopStatus != status.Accepted {
		t.Errorf("top status = %q, want %q", res.TopStatus, status.Accepted)
	}
	if res.Tests[0].Status != status.Accepted {
		t.Errorf("test[0] status = %q, want %q", res.Tests[0].Status, status.Accepted)
	}
}

func TestPy3_WrongOutput(t *testing.T) {
	r := newRunner(ok("world\n"))
	res, err := r.Execute(context.Background(), runner.RunRequest{
		Language: py3,
		Source:   `print("world")`,
		Tests:    []runner.TestCase{{Expected: "hello\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.TopStatus != status.WrongOutput {
		t.Errorf("top status = %q, want %q", res.TopStatus, status.WrongOutput)
	}
}

func TestPy3_WhitespaceMismatch(t *testing.T) {
	r := newRunner(ok("hello  \n"))
	res, err := r.Execute(context.Background(), runner.RunRequest{
		Language: py3,
		Source:   `print("hello  ")`,
		Tests:    []runner.TestCase{{Expected: "hello\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.TopStatus != status.OutputWhitespaceMismatch {
		t.Errorf("top status = %q, want %q", res.TopStatus, status.OutputWhitespaceMismatch)
	}
}

func TestPy3_RuntimeError(t *testing.T) {
	r := newRunner(fail(1))
	res, err := r.Execute(context.Background(), runner.RunRequest{
		Language: py3,
		Source:   `raise ValueError("boom")`,
		Tests:    []runner.TestCase{{Expected: "hello\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.TopStatus != status.RuntimeError {
		t.Errorf("top status = %q, want %q", res.TopStatus, status.RuntimeError)
	}
}

func TestPy3_TimeExceeded(t *testing.T) {
	r := newRunner(timeout())
	res, err := r.Execute(context.Background(), runner.RunRequest{
		Language: py3,
		Source:   `while True: pass`,
		Tests:    []runner.TestCase{{Expected: "hello\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.TopStatus != status.TimeExceeded {
		t.Errorf("top status = %q, want %q", res.TopStatus, status.TimeExceeded)
	}
}

func TestPy3_MemoryExceeded(t *testing.T) {
	r := newRunner(oom())
	res, err := r.Execute(context.Background(), runner.RunRequest{
		Language: py3,
		Source:   `x = [0] * 10**9`,
		Tests:    []runner.TestCase{{Expected: "hello\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.TopStatus != status.MemoryExceeded {
		t.Errorf("top status = %q, want %q", res.TopStatus, status.MemoryExceeded)
	}
}

func TestPy3_MultipleTests_FirstFails(t *testing.T) {
	// first test fails, second passes — top status must be first failure
	r := newRunner(ok("wrong\n"), ok("hello\n"))
	res, err := r.Execute(context.Background(), runner.RunRequest{
		Language: py3,
		Source:   `print("hello")`,
		Tests: []runner.TestCase{
			{Expected: "hello\n"},
			{Expected: "hello\n"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tests[0].Status != status.WrongOutput {
		t.Errorf("test[0] = %q, want wrong_output", res.Tests[0].Status)
	}
	if res.TopStatus != status.WrongOutput {
		t.Errorf("top = %q, want wrong_output", res.TopStatus)
	}
}

// C++ tests — build step fires first, then run per test.

func TestCpp_Accepted(t *testing.T) {
	// build succeeds (exit 0), then run succeeds
	r := newRunner(ok(""), ok("42\n"))
	res, err := r.Execute(context.Background(), runner.RunRequest{
		Language: cpp,
		Source:   `#include<iostream>\nint main(){std::cout<<42<<std::endl;}`,
		Tests:    []runner.TestCase{{Expected: "42\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.BuildStatus != status.BuildOK {
		t.Errorf("build status = %q, want ok", res.BuildStatus)
	}
	if res.TopStatus != status.Accepted {
		t.Errorf("top status = %q, want accepted", res.TopStatus)
	}
}

func TestCpp_BuildFailed(t *testing.T) {
	// build fails — all tests must be not_executed
	r := newRunner(fail(1))
	res, err := r.Execute(context.Background(), runner.RunRequest{
		Language: cpp,
		Source:   `not valid c++`,
		Tests:    []runner.TestCase{{Expected: "42\n"}, {Expected: "0\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.BuildStatus != status.BuildFailed {
		t.Errorf("build status = %q, want build_failed", res.BuildStatus)
	}
	if res.TopStatus != status.BuildFailed {
		t.Errorf("top status = %q, want build_failed", res.TopStatus)
	}
	for i, tr := range res.Tests {
		if tr.Status != status.NotExecuted {
			t.Errorf("test[%d] = %q, want not_executed", i, tr.Status)
		}
	}
}

func TestCpp_WrongOutput(t *testing.T) {
	r := newRunner(ok(""), ok("99\n"))
	res, err := r.Execute(context.Background(), runner.RunRequest{
		Language: cpp,
		Source:   `#include<iostream>\nint main(){std::cout<<99<<std::endl;}`,
		Tests:    []runner.TestCase{{Expected: "42\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.TopStatus != status.WrongOutput {
		t.Errorf("top status = %q, want wrong_output", res.TopStatus)
	}
}
