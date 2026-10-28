//go:build !windows

package softdist

import "os/exec"

// jobGroup is a no-op on non-Windows builds. The softdist package compiles on
// Linux for CI/vet; the real agent is Windows-only in Phase 1, so there is no
// descendant-cleanup requirement here yet. When the agent targets Linux, a
// cgroup-based implementation will land in this file (same API).
type jobGroup struct{}

func newJobGroup(_ string) (*jobGroup, error) { return &jobGroup{}, nil }
func (j *jobGroup) Attach(_ *exec.Cmd) error  { return nil }
func (j *jobGroup) Terminate(_ uint32) error  { return nil }
func (j *jobGroup) Close() error              { return nil }
func (j *jobGroup) activeDescendants() int    { return 0 }
