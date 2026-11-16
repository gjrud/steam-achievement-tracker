//go:build !windows

package fsretry

// isTransient is never true off Windows, so the wrappers make one os call there.
func isTransient(error) bool {
	return false
}
