package flags_test

import (
	"testing"

	"github.com/ambitious-scrap/goboxd/internal/flags"
)

var allowlist = []string{"-O0", "-O1", "-O2", "-O3", "-Wall", "-Wextra", "-std=*", "-g"}

func TestValidate(t *testing.T) {
	cases := []struct {
		flags   []string
		wantErr bool
	}{
		{[]string{"-O2", "-Wall"}, false},
		{[]string{"-std=c++17"}, false},
		{[]string{"-std=c99"}, false},
		{[]string{"-g"}, false},
		{[]string{"-O5"}, true},              // not in allowlist
		{[]string{"-fplugin=evil.so"}, true}, // blocked prefix
		{[]string{"@flags.rsp"}, true},       // response file
		{[]string{"-Wl,-rpath,/evil"}, true}, // blocked prefix
		{[]string{"-B/evil"}, true},          // blocked prefix
		{[]string{"-x", "c"}, true},          // blocked prefix
		{[]string{}, false},                  // empty is fine
	}
	for _, tc := range cases {
		err := flags.Validate(tc.flags, allowlist)
		if (err != nil) != tc.wantErr {
			t.Errorf("Validate(%v) err=%v, wantErr=%v", tc.flags, err, tc.wantErr)
		}
	}
}
