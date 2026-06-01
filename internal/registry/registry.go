package registry

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/thesouldev/goboxd/internal/config"
)

// SmokeResult holds the outcome of a language smoke probe.
type SmokeResult struct {
	OK      bool
	Version string
	Error   string
}

// Registry is the in-memory language lookup table built from config at startup.
type Registry struct {
	langs map[string]*config.Language
}

// New builds a Registry from the loaded language list.
func New(langs []config.Language) *Registry {
	r := &Registry{langs: make(map[string]*config.Language, len(langs))}
	for i := range langs {
		l := langs[i]
		r.langs[l.ID] = &l
	}
	return r
}

// Get looks up a language by id.
func (r *Registry) Get(id string) (*config.Language, bool) {
	l, ok := r.langs[id]
	return l, ok
}

// All returns all registered languages in deterministic order (sorted by id).
func (r *Registry) All() []*config.Language {
	ids := make([]string, 0, len(r.langs))
	for id := range r.langs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]*config.Language, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.langs[id])
	}
	return out
}

// RunSmokes executes every language's smoke probe and returns results keyed by id.
func (r *Registry) RunSmokes(ctx context.Context) map[string]SmokeResult {
	results := make(map[string]SmokeResult, len(r.langs))
	for id, lang := range r.langs {
		results[id] = runSmoke(ctx, lang)
	}
	return results
}

func runSmoke(ctx context.Context, lang *config.Language) SmokeResult {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	args := Resolve(lang.Smoke.Args, nil)
	cmd := exec.CommandContext(ctx, lang.Smoke.Cmd, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		return SmokeResult{OK: false, Error: fmt.Sprintf("%v: %s", err, strings.TrimSpace(out.String()))}
	}
	version := strings.TrimSpace(out.String())
	if len(version) > 120 {
		version = version[:120]
	}
	return SmokeResult{OK: true, Version: version}
}
