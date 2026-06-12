//go:build integration

// Package tests_test integration suite: executes a trivial program through the
// real nsjail sandbox for every configured language, verifying that the binary
// paths, build/run commands and runtimes in configs/languages.yaml actually
// work in the built image. These tests need the real toolchains and nsjail, so
// they only run inside the Docker image (or a host with everything installed)
// and are gated behind the `integration` build tag:
//
//	make integration        # go test ./tests/... -tags integration -v
//
// The fake-sandbox unit tests in runner_e2e_test.go cover status mapping; this
// suite covers "does the configured command actually produce output".
package tests_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/thesouldev/goboxd/internal/config"
	"github.com/thesouldev/goboxd/internal/registry"
	"github.com/thesouldev/goboxd/internal/runner"
	"github.com/thesouldev/goboxd/internal/status"
)

// helloCase is a per-language hello-world program and its expected stdout.
type helloCase struct {
	id               string
	source           string
	sourceFilename   string // only for from_request languages (e.g. Java)
	artifactFilename string // only for from_request languages (e.g. Java)
	expected         string
}

var helloCases = []helloCase{
	{id: "py3", source: "print(\"hello\")\n", expected: "hello\n"},
	{id: "bash", source: "echo hello\n", expected: "hello\n"},
	{id: "javascript", source: "console.log(\"hello\")\n", expected: "hello\n"},
	{id: "c", source: "#include <stdio.h>\nint main(void){printf(\"hello\\n\");return 0;}\n", expected: "hello\n"},
	{id: "cpp", source: "#include <iostream>\nint main(){std::cout<<\"hello\\n\";}\n", expected: "hello\n"},
	{
		id:               "java",
		source:           "public class Main { public static void main(String[] a){ System.out.println(\"hello\"); } }\n",
		sourceFilename:   "Main.java",
		artifactFilename: "Main",
		expected:         "hello\n",
	},
	{id: "verilog", source: "module main;\ninitial begin\n  $display(\"hello\");\n  $finish;\nend\nendmodule\n", expected: "hello\n"},
	{id: "rust", source: "fn main() { println!(\"hello\"); }\n", expected: "hello\n"},
	{id: "elixir", source: "IO.puts(\"hello\")\n", expected: "hello\n"},
	{id: "powershell", source: "Write-Output \"hello\"\n", expected: "hello\n"},
}

func TestLanguages_HelloWorld(t *testing.T) {
	// Config path is overridable so the suite can run both from the repo root
	// (go test ./tests/...) and inside the runtime image (GOBOXD_CONFIG points at
	// the installed config). See the integration-docker Makefile target.
	cfgPath := os.Getenv("GOBOXD_CONFIG")
	if cfgPath == "" {
		cfgPath = "../configs/languages.yaml"
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := os.MkdirAll(cfg.Server.JailBase, 0o700); err != nil {
		t.Fatalf("create jail base: %v", err)
	}
	reg := registry.New(cfg.Languages)
	r := runner.New(cfg.Server.NsjailPath, cfg.Server.JailBase, cfg.Server.OutputCapBytes, 0, nil)

	for _, tc := range helloCases {
		tc := tc
		t.Run(tc.id, func(t *testing.T) {
			lang, ok := reg.Get(tc.id)
			if !ok {
				t.Skipf("language %s not in registry", tc.id)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			res, err := r.Execute(ctx, runner.RunRequest{
				Language:         lang,
				Source:           tc.source,
				SourceFilename:   tc.sourceFilename,
				ArtifactFilename: tc.artifactFilename,
				Tests:            []runner.TestCase{{Expected: tc.expected}},
			})
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if res.TopStatus != status.Accepted {
				t.Fatalf("top status = %q, want accepted\n  build: %s\n  build_stderr: %s\n  stdout: %q\n  stderr: %q",
					res.TopStatus, res.BuildStatus, res.BuildStderr,
					res.Tests[0].Stdout, res.Tests[0].Stderr)
			}
		})
	}
}
