package registry

import "strings"

// Resolve replaces {{key}} placeholders in args with values from vars.
// Unknown placeholders are left as-is.
func Resolve(args []string, vars map[string]string) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		s := arg
		for k, v := range vars {
			s = strings.ReplaceAll(s, "{{"+k+"}}", v)
		}
		out[i] = s
	}
	return out
}
