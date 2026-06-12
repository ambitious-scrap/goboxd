package sandbox

import (
	"strings"
	"testing"

	"github.com/thesouldev/goboxd/internal/config"
)

// FuzzBuildNsjailArgs asserts the core sandbox safety invariant: no matter what
// a user puts in the command or its arguments, those tokens are positionally
// isolated after nsjail's "--" separator and can never be interpreted as nsjail
// control flags. The builder appends exactly: ... "--" Cmd Args...; this fuzz
// confirms that holds for arbitrary input (flag-injection guard).
func FuzzBuildNsjailArgs(f *testing.F) {
	f.Add("/bin/true", "", "off", "")
	f.Add("--rw", "--seccomp_string\x1f-B/etc", "enforce", "POLICY p { ALLOW { read } } USE p DEFAULT KILL")
	f.Add("./{{artifact}}", "--time_limit\x1f99999", "audit", "")
	f.Add("", "\x1f\x1f", "off", "")

	f.Fuzz(func(t *testing.T, cmd, argsBlob, mode, policy string) {
		var userArgs []string
		if argsBlob != "" {
			userArgs = strings.Split(argsBlob, "\x1f")
		}
		cfg := RunConfig{
			LanguageID:    "py3",
			WorkDir:       "/jail/fuzz",
			Cmd:           cmd,
			Args:          userArgs,
			Limits:        config.Limits{WallTimeS: 3, MemoryKB: 65536, MaxProcesses: 16},
			SeccompMode:   mode,
			SeccompPolicy: policy,
		}

		args := buildNsjailArgs(cfg, &cgroup{ok: false})

		// The builder always appends, at the very end: "--", Cmd, Args...
		// Anchor on the tail (not the first "--", which a seccomp policy value
		// could itself equal): the last len(want) elements must be exactly the
		// user command + args, immediately preceded by the "--" separator. This
		// proves user input is never spliced into the nsjail flag region.
		want := append([]string{cmd}, userArgs...)
		if len(args) < len(want)+1 {
			t.Fatalf("args too short to hold separator + user cmd: %v", args)
		}
		sep := len(args) - len(want) - 1
		if args[sep] != "--" {
			t.Fatalf("expected -- separator before user segment at %d: %v", sep, args)
		}
		tail := args[sep+1:]
		for i := range want {
			if tail[i] != want[i] {
				t.Fatalf("tail[%d]=%q want %q (args=%v)", i, tail[i], want[i], args)
			}
		}
	})
}
