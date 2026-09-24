package ipcbus

import (
	"bufio"
	"testing"
	"time"
)

func TestPingPong(t *testing.T) {
	l, err := NewListener()
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer l.Close()

	done := make(chan string, 1)

	go func() {
		conn, err := l.Accept()
		if err != nil {
			done <- "SERVER_ERROR: " + err.Error()
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		msg, _ := reader.ReadString('\n')
		conn.Write([]byte("pong\n"))
		done <- msg
	}()

	time.Sleep(300 * time.Millisecond)

	conn, err := Dial()
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	conn.Write([]byte("ping\n"))
	reader := bufio.NewReader(conn)
	reply, _ := reader.ReadString('\n')

	select {
	case serverGot := <-done:
		if serverGot != "ping\n" {
			t.Errorf("server expected 'ping', got: %q", serverGot)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server to receive message")
	}

	if reply != "pong\n" {
		t.Errorf("client expected 'pong', got: %q", reply)
	}
}