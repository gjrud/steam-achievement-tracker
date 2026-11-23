//go:build !windows

package update

// syncInstalledVersion is a no-op off Windows: Apps & Features is a Windows list, and the other platforms have no
// installer entry to update.
func syncInstalledVersion(exe, cur string) {}
