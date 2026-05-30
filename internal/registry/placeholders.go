package registry

import "strings"

// Resolve replaces {{key}} placeholders in args with values from vars.
// Unknown placeholders (including {{flags}}) are left as-is.
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

// ResolveOne replaces {{key}} placeholders in a single string. Used for the
// run/build command, e.g. "./{{artifact}}" -> "./a.out". Unknown placeholders
// are left as-is.
func ResolveOne(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	return s
}

// ExpandFlags replaces a standalone {{flags}} element in args with the
// individual flag strings. If no {{flags}} element is present, flags are
// prepended to args. Call after Resolve so other placeholders are already
// expanded.
func ExpandFlags(args []string, flags []string) []string {
	if len(flags) == 0 {
		// Remove any bare {{flags}} element when no flags were provided.
		out := args[:0:len(args)]
		for _, a := range args {
			if a != "{{flags}}" {
				out = append(out, a)
			}
		}
		return out
	}
	for i, arg := range args {
		if arg == "{{flags}}" {
			out := make([]string, 0, len(args)-1+len(flags))
			out = append(out, args[:i]...)
			out = append(out, flags...)
			out = append(out, args[i+1:]...)
			return out
		}
	}
	// No placeholder — prepend flags.
	return append(flags, args...)
}
