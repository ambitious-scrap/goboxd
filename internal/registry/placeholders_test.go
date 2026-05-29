package registry_test

import (
	"reflect"
	"testing"

	"github.com/ambitious-scrap/goboxd/internal/registry"
)

func TestExpandFlags(t *testing.T) {
	cases := []struct {
		args  []string
		flags []string
		want  []string
	}{
		// placeholder present, flags provided
		{
			args:  []string{"{{flags}}", "-o", "solution", "solution.cpp"},
			flags: []string{"-O2", "-Wall"},
			want:  []string{"-O2", "-Wall", "-o", "solution", "solution.cpp"},
		},
		// placeholder present, no flags — placeholder removed
		{
			args:  []string{"{{flags}}", "-o", "solution", "solution.cpp"},
			flags: nil,
			want:  []string{"-o", "solution", "solution.cpp"},
		},
		// no placeholder, flags prepended
		{
			args:  []string{"-o", "solution", "solution.cpp"},
			flags: []string{"-O2"},
			want:  []string{"-O2", "-o", "solution", "solution.cpp"},
		},
		// no placeholder, no flags — unchanged
		{
			args:  []string{"-o", "solution"},
			flags: nil,
			want:  []string{"-o", "solution"},
		},
	}
	for _, tc := range cases {
		got := registry.ExpandFlags(tc.args, tc.flags)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ExpandFlags(%v, %v) = %v, want %v", tc.args, tc.flags, got, tc.want)
		}
	}
}

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
