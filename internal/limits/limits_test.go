package limits_test

import (
	"testing"

	"github.com/ambitious-scrap/goboxd/internal/config"
	"github.com/ambitious-scrap/goboxd/internal/limits"
)

func intp(v int) *int { return &v }

func TestMerge(t *testing.T) {
	defaults := config.Limits{WallTimeS: 5, MemoryKB: 262144, MaxProcesses: 64}

	cases := []struct {
		name     string
		override *limits.RequestOverride
		want     config.Limits
	}{
		{
			name:     "nil override returns defaults",
			override: nil,
			want:     defaults,
		},
		{
			name:     "empty override returns defaults",
			override: &limits.RequestOverride{},
			want:     defaults,
		},
		{
			name:     "in-range values applied",
			override: &limits.RequestOverride{WallTimeS: intp(3), MemoryKB: intp(131072), MaxProcesses: intp(32)},
			want:     config.Limits{WallTimeS: 3, MemoryKB: 131072, MaxProcesses: 32},
		},
		{
			name:     "equal to max is allowed",
			override: &limits.RequestOverride{WallTimeS: intp(5), MemoryKB: intp(262144), MaxProcesses: intp(64)},
			want:     config.Limits{WallTimeS: 5, MemoryKB: 262144, MaxProcesses: 64},
		},
		{
			name:     "over max is clamped to default",
			override: &limits.RequestOverride{WallTimeS: intp(99), MemoryKB: intp(9999999), MaxProcesses: intp(9999)},
			want:     defaults,
		},
		{
			name:     "zero is ignored",
			override: &limits.RequestOverride{WallTimeS: intp(0), MemoryKB: intp(0), MaxProcesses: intp(0)},
			want:     defaults,
		},
		{
			name:     "negative is ignored",
			override: &limits.RequestOverride{WallTimeS: intp(-1), MemoryKB: intp(-100), MaxProcesses: intp(-5)},
			want:     defaults,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := limits.Merge(defaults, tc.override)
			if got != tc.want {
				t.Errorf("Merge() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
