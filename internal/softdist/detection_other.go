//go:build !windows

package softdist

// ProductInstalled is Windows-only (MSI product state). Elsewhere it reports
// false, so deploys are not wrongly skipped.
func ProductInstalled(string) bool { return false }
