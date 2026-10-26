//go:build !windows

package softdist

import (
	"fmt"
	"os/exec"
)

type Session struct{}

func OpenSession(Execution) (*Session, error) {
	return nil, fmt.Errorf("execution contexts require Windows")
}
func (*Session) Close() error            { return nil }
func (*Session) DetectionSID() string    { return "" }
func (*Session) Prepare(*exec.Cmd) error { return fmt.Errorf("execution contexts require Windows") }
func (*Session) StagePayload(string, string) (string, func(), error) {
	return "", nil, fmt.Errorf("execution contexts require Windows")
}
