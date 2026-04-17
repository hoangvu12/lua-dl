//go:build !windows

package goldberg

import "errors"

func isDefenderError(_ error) bool           { return false }
func addDefenderExclusion(_ string) error    { return errors.New("not supported") }
