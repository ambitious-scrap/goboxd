package runner

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/thesouldev/goboxd/internal/artifactcache"
	"github.com/thesouldev/goboxd/internal/config"
	"github.com/thesouldev/goboxd/internal/sandbox"
)

const buildCmd = "/usr/bin/g++"

func cppLang() *config.Language {
	return &config.Language{
		ID:             "cpp",
		Name:           "C++",
		SourceFilename: "solution.cpp",
		Artifact:       "a.out",
		Build: &config.BuildStep{
			Cmd:    buildCmd,
			Args:   []string{"-o", "{{artifact}}", "{{source}}"},
			Limits: config.Limits{WallTimeS: 10, MemoryKB: 1048576, MaxProcesses: 100},
		},
		Run: config.RunStep{
			Cmd:    "./{{artifact}}",
			Limits: config.Limits{WallTimeS: 5, MemoryKB: 262144, MaxProcesses: 64},
		},
	}
}

// countingSandbox records how many build vs run invocations it served. Build
// invocations are distinguished by the configured build command.
type countingSandbox struct {
	mu     sync.Mutex
	builds int
	runs   int
}

func (c *countingSandbox) Run(_ context.Context, cfg sandbox.RunConfig) (*sandbox.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cfg.Cmd == buildCmd {
		c.builds++
		return &sandbox.Result{ExitCode: 0, Stdout: "compiled", Stderr: "warning"}, nil
	}
	c.runs++
	return &sandbox.Result{ExitCode: 0, Stdout: "hi\n"}, nil
}

func cppReq() RunRequest {
	return RunRequest{
		Language:         cppLang(),
		Source:           "int main(){}",
		ToolchainVersion: "g++ 13.2",
		Tests:            []TestCase{{Stdin: "", Expected: "hi\n"}},
	}
}

// A second identical submission must serve the artifact from cache: zero builds,
// CacheStatus="hit", with the original build output and duration replayed.
func TestExecute_CacheHitSkipsBuild(t *testing.T) {
	cache, err := artifactcache.New(t.TempDir(), 16)
	if err != nil {
		t.Fatal(err)
	}
	sb := &countingSandbox{}
	r := NewWithSandbox(sb, t.TempDir(), 65536, 0, cache)

	first, err := r.Execute(context.Background(), cppReq())
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if first.CacheStatus != "miss" {
		t.Errorf("first CacheStatus = %q, want miss", first.CacheStatus)
	}
	if sb.builds != 1 {
		t.Fatalf("first run builds = %d, want 1", sb.builds)
	}

	second, err := r.Execute(context.Background(), cppReq())
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	if second.CacheStatus != "hit" {
		t.Errorf("second CacheStatus = %q, want hit", second.CacheStatus)
	}
	if sb.builds != 1 {
		t.Errorf("second run triggered a build: builds = %d, want still 1", sb.builds)
	}
	// Build output + duration are replayed from the cached meta.
	if second.BuildStatus != "ok" {
		t.Errorf("second BuildStatus = %q, want ok", second.BuildStatus)
	}
	if second.BuildStdout != "compiled" || second.BuildStderr != "warning" {
		t.Errorf("build output not replayed: stdout=%q stderr=%q", second.BuildStdout, second.BuildStderr)
	}
	if second.BuildDurationMs != first.BuildDurationMs {
		t.Errorf("replayed duration = %d, want %d", second.BuildDurationMs, first.BuildDurationMs)
	}
	// The run phase still executes live every time.
	if sb.runs != 2 {
		t.Errorf("runs = %d, want 2 (one per Execute)", sb.runs)
	}
}

// An unknown toolchain version must skip the cache entirely and always build.
func TestExecute_EmptyToolchainSkipsCache(t *testing.T) {
	cache, err := artifactcache.New(t.TempDir(), 16)
	if err != nil {
		t.Fatal(err)
	}
	sb := &countingSandbox{}
	r := NewWithSandbox(sb, t.TempDir(), 65536, 0, cache)

	req := cppReq()
	req.ToolchainVersion = ""

	for i := 0; i < 2; i++ {
		res, err := r.Execute(context.Background(), req)
		if err != nil {
			t.Fatalf("execute %d: %v", i, err)
		}
		if res.CacheStatus != "" {
			t.Errorf("CacheStatus = %q, want empty (cache not consulted)", res.CacheStatus)
		}
	}
	if sb.builds != 2 {
		t.Errorf("builds = %d, want 2 (no caching)", sb.builds)
	}
}

// blockingSandbox signals on every build entry and blocks the build until
// released, so the build lane's serialization can be observed.
type blockingSandbox struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingSandbox) Run(_ context.Context, cfg sandbox.RunConfig) (*sandbox.Result, error) {
	if cfg.Cmd == buildCmd {
		b.entered <- struct{}{}
		<-b.release
	}
	return &sandbox.Result{ExitCode: 0, Stdout: "hi\n"}, nil
}

// With a build lane of size 1, a second concurrent build must block until the
// first releases its token.
func TestExecute_BuildLaneSerializes(t *testing.T) {
	sb := &blockingSandbox{entered: make(chan struct{}), release: make(chan struct{})}
	r := NewWithSandbox(sb, t.TempDir(), 65536, 1, nil) // build lane = 1, no cache

	go func() { r.Execute(context.Background(), cppReq()) }()
	// First build enters and holds the only build token.
	<-sb.entered

	go func() { r.Execute(context.Background(), cppReq()) }()
	// The second build must NOT enter while the token is held.
	select {
	case <-sb.entered:
		t.Fatal("second build entered while build lane was full")
	case <-time.After(100 * time.Millisecond):
	}

	// Release the first build; the second can now acquire the token and enter.
	sb.release <- struct{}{}
	select {
	case <-sb.entered:
	case <-time.After(time.Second):
		t.Fatal("second build did not enter after token freed")
	}
	sb.release <- struct{}{}
}
