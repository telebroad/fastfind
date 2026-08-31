//go:build !windows

package walk

import "io/fs"

// isReparsePoint is Windows' problem: elsewhere a symlink is a symlink and the
// walker's own `fs.ModeSymlink` check has already handled it.
func isReparse(fs.FileInfo) bool { return false }
