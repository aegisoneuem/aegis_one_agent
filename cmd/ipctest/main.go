package main

import (
	"bufio"
	"fmt"
	"os"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/ipcbus"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "client" {
		runClient()
		return
	}
	runServer()
}

func runServer() {
	l, err := ipcbus.NewListener()
	if err != nil {
		fmt.Println("Failed to create listener:", err)
		return
	}
	defer l.Close()
	fmt.Println("IPC server listening... waiting for a client")

	conn, err := l.Accept()
	if err != nil {
		fmt.Println("Accept failed:", err)
		return
	}
	defer conn.Close()
	fmt.Println("Client connected!")

	reader := bufio.NewReader(conn)
	msg, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println("Read failed:", err)
		return
	}
	fmt.Println("Server received:", msg)

	conn.Write([]byte("pong\n"))
	fmt.Println("Server sent: pong")
}

func runClient() {
	time.Sleep(1 * time.Second) // give the server a moment to start listening
	conn, err := ipcbus.Dial()
	if err != nil {
		fmt.Println("Dial failed:", err)
		return
	}
	defer conn.Close()

	conn.Write([]byte("ping\n"))
	fmt.Println("Client sent: ping")

	reader := bufio.NewReader(conn)
	msg, _ := reader.ReadString('\n')
	fmt.Println("Client received:", msg)
}