//go:build !windows

package ipcbus

import (
	"net"
	"os"
)

func NewListener() (Listener, error) {
	sockPath := "/tmp/" + PipeName + ".sock"
	os.Remove(sockPath)
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	return l, nil
}