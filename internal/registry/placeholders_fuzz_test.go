package registry_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/thesouldev/goboxd/internal/registry"
)

// FuzzResolveOne checks the placeholder substitution never panics and that it
// remains exactly a single-pass {{key}} -> value replacement. (A single pass
// can legitimately form a *new* token from adjacent text, e.g. "{{{{x}}x}}"
// with x->"" yields "{{x}}"; that is correct ReplaceAll behavior, so the
// invariant is equivalence to one ReplaceAll, not token absence.)
func FuzzResolveOne(f *testing.F) {
	f.Add("./{{artifact}}", "artifact", "a.out")
	f.Add("{{{{x}}x}}", "x", "")
	f.Add("no placeholders here", "k", "v")
	f.Add("{{k}}", "k", "{{k}}")

	f.Fuzz(func(t *testing.T, s, k, v string) {
		got := registry.ResolveOne(s, map[string]string{k: v})
		want := strings.ReplaceAll(s, "{{"+k+"}}", v)
		if got != want {
			t.Fatalf("ResolveOne(%q,{%q:%q})=%q want single-pass %q", s, k, v, got, want)
		}
	})
}

// FuzzExpandFlags checks flag expansion never panics and preserves all flags:
// when flags are provided they always appear in the output, whether a
// {{flags}} placeholder is present (replaced) or absent (prepended).
func FuzzExpandFlags(f *testing.F) {
	f.Add("{{flags}}\x1f-o\x1fout", "-O2\x1f-Wall")
	f.Add("-o\x1fout", "")
	f.Add("{{flags}}", "")
	f.Add("a\x1f{{flags}}\x1fb\x1f{{flags}}", "-x")

	f.Fuzz(func(t *testing.T, argsBlob, flagsBlob string) {
		var args, flags []string
		if argsBlob != "" {
			args = strings.Split(argsBlob, "\x1f")
		}
		if flagsBlob != "" {
			flags = strings.Split(flagsBlob, "\x1f")
		}

		out := registry.ExpandFlags(slices.Clone(args), flags)

		// Every provided flag must survive into the output.
		for _, fl := range flags {
			if !slices.Contains(out, fl) {
				t.Fatalf("flag %q missing from output: args=%v flags=%v out=%v", fl, args, flags, out)
			}
		}
		// With no flags, no bare {{flags}} placeholder may remain.
		if len(flags) == 0 && slices.Contains(out, "{{flags}}") {
			t.Fatalf("bare {{flags}} survived with no flags: out=%v", out)
		}
	})
}
