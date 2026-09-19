package main

import (
	"fmt"
	"github.com/aegisoneuem/aegis_one_agent/internal/config"
)

func main() {
	cfg := config.Load()
	fmt.Println("Aegis One Agent - starting up")
	fmt.Println("Server address:", cfg.ServerAddress)
	fmt.Println("Log level:", cfg.LogLevel)
}