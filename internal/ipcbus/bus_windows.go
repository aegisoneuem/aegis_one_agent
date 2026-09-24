//go:build windows

package ipcbus

import (
	"github.com/Microsoft/go-winio"
	"net"
)

func NewListener() (Listener, error) {
	pipePath := `\\.\pipe\` + PipeName
	l, err := winio.ListenPipe(pipePath, nil)
	if err != nil {
		return nil, err
	}
	return &winioListener{l}, nil
}

type winioListener struct {
	l net.Listener
}

func (w *winioListener) Accept() (net.Conn, error) { return w.l.Accept() }
func (w *winioListener) Close() error               { return w.l.Close() }

func Dial() (net.Conn, error) {
	pipePath := `\\.\pipe\` + PipeName
	return winio.DialPipe(pipePath, nil)
}