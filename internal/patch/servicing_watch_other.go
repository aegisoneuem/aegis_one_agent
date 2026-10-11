//go:build !windows

package patch

import "context"

// WatchServicing is Windows-only (CBS registry notifications). On other platforms
// it blocks until ctx is done and never fires.
func WatchServicing(ctx context.Context, fire chan<- struct{}) {
	<-ctx.Done()
}
