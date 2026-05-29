package registry_test

import (
	"testing"

	"github.com/ambitious-scrap/goboxd/internal/registry"
)

func TestResolve(t *testing.T) {
	cases := []struct {
		args []string
		vars map[string]string
		want []string
	}{
		{
			args: []string{"-o", "{{artifact}}", "{{source}}"},
			vars: map[string]string{"artifact": "solution", "source": "solution.cpp"},
			want: []string{"-o", "solution", "solution.cpp"},
		},
		{
			args: []string{"{{source}}"},
			vars: map[string]string{"source": "solution.py"},
			want: []string{"solution.py"},
		},
		{
			// unknown placeholder left as-is
			args: []string{"{{unknown}}"},
			vars: map[string]string{},
			want: []string{"{{unknown}}"},
		},
		{
			args: []string{},
			vars: map[string]string{"source": "foo.py"},
			want: []string{},
		},
	}
	for _, tc := range cases {
		got := registry.Resolve(tc.args, tc.vars)
		if len(got) != len(tc.want) {
			t.Errorf("Resolve(%v) = %v, want %v", tc.args, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("Resolve(%v)[%d] = %q, want %q", tc.args, i, got[i], tc.want[i])
			}
		}
	}
}
