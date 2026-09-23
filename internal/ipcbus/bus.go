package ipcbus

import "net"

// Listener abstracts the platform-specific IPC mechanism
// (named pipe on Windows, Unix domain socket elsewhere).
type Listener interface {
	Accept() (net.Conn, error)
	Close() error
}

const PipeName = "aegisone-ipc"