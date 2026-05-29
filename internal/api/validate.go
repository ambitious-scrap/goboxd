package api

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var validFilename = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validateFilename rejects path traversal and shell-hostile names.
func validateFilename(name string) error {
	if name == "" {
		return fmt.Errorf("filename is empty")
	}
	if !validFilename.MatchString(name) {
		return fmt.Errorf("filename %q contains invalid characters", name)
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("filename %q must not start with a dot", name)
	}
	if filepath.Base(name) != name {
		return fmt.Errorf("filename %q must be a single path component", name)
	}
	return nil
}
