//go:build windows

package walk

import (
	"io/fs"
	"syscall"
)

// Deciding whether to descend, without paying for a second look at the file.
//
// Windows already told us. `os.ReadDir` is backed by FindFirstFile/FindNextFile,
// which return the full attribute word for every entry, and Go keeps it: the
// `FileInfo` from a `DirEntry` is filled in from that scan rather than from a
// fresh stat. So the reparse bit is sitting in memory already, and asking the
// filesystem again with GetFileAttributes is a syscall per directory for an
// answer we were handed for free.
//
// That mattered more than it sounds. On a walk of a 1.9-million-entry profile
// the extra call was most of the wall clock.
//
// The bit itself is what has to be checked, because Go's `fs.ModeSymlink` does
// NOT cover directory junctions — and junctions are exactly what Windows leaves
// all over a user profile. "Application Data" and "My Documents" point back at
// their own parents, so a walk that follows them never ends. The virtual trees
// Google Drive and OneDrive project are reparse points too, and descending into
// one asks that service to materialise every file it is holding in the cloud.

// isReparse reports whether a directory entry is a junction, symlink or a
// virtual filesystem's projection, reading the attributes already fetched.
func isReparse(info fs.FileInfo) bool {
	if info == nil {
		// Nothing to judge it by, so do not go in. A directory that cannot even
		// be described is not one worth descending into.
		return true
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return true
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return false
	}
	return data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
