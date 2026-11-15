//go:build !windows

package main

// Non-Windows stub: disk-performance sampling is Windows/PDH-specific. sample()
// always yields OK=false so callers simply omit disk metrics on other platforms.
type diskSample struct {
	QueueLength float64
	IOPS        float64
	LatencyMs   float64
	OK          bool
}

type diskSampler struct{}

func newDiskSampler() *diskSampler  { return &diskSampler{} }
func (d *diskSampler) sample() diskSample { return diskSample{} }
func (d *diskSampler) close()       {}
