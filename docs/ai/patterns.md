# Patterns

Idioms that came up during this project, with the specific file/function where they're applied. Mostly Go patterns that weren't obvious upfront.

---

## Interface injection for subprocess isolation

**Where:** `internal/runner/runner.go` — `SandboxRunner` interface; `tests/runner_e2e_test.go` — `fakeSandbox`

When your code's interesting logic is separated from its side effects by a narrow interface, tests can inject a fake that replays scripted results without touching the filesystem. The key is making the interface as small as possible — one method here:

```go
type SandboxRunner interface {
    Run(ctx context.Context, cfg sandbox.RunConfig) (*sandbox.Result, error)
}
```

The fake:

```go
type fakeSandbox struct {
    results []*sandbox.Result
    idx     int
}

func (f *fakeSandbox) Run(_ context.Context, _ sandbox.RunConfig) (*sandbox.Result, error) {
    r := f.results[f.idx]
    f.idx++
    return r, nil
}
```

Call `newRunner(ok("hello\n"), fail(1), timeout())` to script a multi-step test sequence. The runner doesn't know it's talking to a fake.

This pattern is worth using any time the thing you're wrapping (nsjail, a network call, a database) is slow, stateful, or has external dependencies.

---

## Two-pass argv placeholder expansion

**Where:** `internal/registry/placeholders.go` — `Resolve()` and `ExpandFlags()`

Replacing one placeholder with multiple argv elements can't be done in a single range-and-assign loop. Two-pass approach:

Pass 1 (`Resolve`): replace scalar placeholders (`{{source}}`, `{{artifact}}`, `{{workdir}}`). Skip `{{flags}}` — leave the marker in place.

Pass 2 (`ExpandFlags`): rebuild the slice. When an element equals `{{flags}}`, append all flag strings individually. Otherwise append as-is.

```go
// pass 1
resolved := Resolve(args, vars)  // {{source}} → /path/solution.py; {{flags}} left alone

// pass 2
expanded := ExpandFlags(resolved, flags)  // {{flags}} → ["-O2", "-std=c++17"]
```

This generalizes: if you ever need a placeholder that expands to N elements, the two-pass pattern handles it cleanly. Single-pass with index manipulation gets messy fast.

---

## `io.LimitReader` + truncation marker

**Where:** `internal/sandbox/sandbox.go` — `limitedWriter`

Unbounded subprocess output can exhaust host memory if user code writes to stdout in a loop. Cap with `io.LimitReader` and mark truncated output so callers know they're not seeing the full thing:

```go
type limitedWriter struct {
    buf       *bytes.Buffer
    remaining int
    truncated bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
    if w.remaining <= 0 {
        w.truncated = true
        return len(p), nil  // discard, pretend success to avoid broken pipe
    }
    n := min(len(p), w.remaining)
    w.buf.Write(p[:n])
    w.remaining -= n
    if w.remaining == 0 {
        w.truncated = true
    }
    return len(p), nil
}
```

After capture, if `truncated`, append `\n...[truncated]` to stdout. The caller (runner) gets a clean string regardless.

The "pretend success" return is intentional — returning an error causes the subprocess to get a broken pipe signal, which changes its exit code and corrupts the status mapping.

---

## Atomic counter + PID + rand suffix for unique names

**Where:** `internal/jail/jail.go` — `Create()`

Need unique directory names that survive concurrent requests and process restarts. Three components each solving a different collision case:

- **Atomic counter:** prevents same-goroutine collisions within a single process. Increments monotonically, no locking.
- **PID:** prevents cross-restart collisions. If the process dies and restarts, old directories have the old PID. `SweepOrphans` can clean them by age without knowing the old PID.
- **Crypto/rand hex:** prevents brute-force guessing of directory names (mild defense-in-depth; the real isolation is nsjail).

```go
var counter int64

func Create(base string) (string, error) {
    suffix, _ := randHex(8)
    name := fmt.Sprintf("%d_%d_%s", atomic.AddInt64(&counter, 1), os.Getpid(), suffix)
    path := filepath.Join(base, name)
    if err := os.Mkdir(path, 0700); err != nil {
        return "", err
    }
    return path, nil
}
```

Use `os.Mkdir` not `os.MkdirAll` for the final component — you want an error if the name already exists, not silent success.

---

## Flag allow-list with glob pattern matching

**Where:** `internal/flags/flags.go` — `Validate()`

Compiler flags can't be validated by exact string match because patterns like `-std=c++17` and `-std=c++20` are both valid but `-std=arbitrary` is not. Use `filepath.Match` for glob patterns in the allowlist:

```go
for _, pattern := range allowlist {
    if pattern == flag {
        return nil  // exact match
    }
    if matched, _ := filepath.Match(pattern, flag); matched {
        return nil  // glob match: "-std=*" matches "-std=c++17"
    }
}
return fmt.Errorf("flag %q not in allowlist", flag)
```

Before allow-list matching, reject known dangerous prefixes unconditionally — `-fplugin=`, `--specs=`, `-Wl,`, `-x`, `-B`, `@`. These bypass the intent of the allow-list by invoking external code or changing where the compiler looks for things.

The prefix check runs before the allow-list check so a pattern like `-B*` in the allowlist can't accidentally re-allow `-B` variants.

---

## An `io.Writer` wrapper must report the full input length

**Where:** `internal/sandbox/sandbox.go` — `limitedWriter`

When you wrap an `io.Writer` to cap or filter output and that wrapper is used as a subprocess's `cmd.Stdout`, `os/exec` drives it with `io.Copy`. `io.Copy` interprets a return of `n < len(p)` with a nil error as `io.ErrShortWrite` and aborts the copy, which then fails `cmd.Run`. So a capping writer must not "write less and report less" — it must consume `len(p)` from the caller's point of view and drop the overflow internally:

```go
func (lw *limitedWriter) Write(p []byte) (int, error) {
    if lw.remaining <= 0 {
        lw.truncated = true
        return len(p), nil // swallow, report full length
    }
    if int64(len(p)) > lw.remaining {
        lw.truncated = true
        if _, err := lw.w.Write(p[:lw.remaining]); err != nil {
            return 0, err
        }
        lw.remaining = 0
        return len(p), nil // report len(p), not the partial write
    }
    n, err := lw.w.Write(p)
    lw.remaining -= int64(n)
    return n, err
}
```

Return a short count only when you actually mean to signal an error (with a non-nil error). Anything else is a latent `ErrShortWrite` waiting for an input size that doesn't align with `io.Copy`'s 32 KiB buffer.
