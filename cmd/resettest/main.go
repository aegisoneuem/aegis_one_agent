package main

import (
	"fmt"

	"github.com/aegisoneuem/aegis_one_agent/internal/patch"
)

func main() {
	fmt.Println("Testing WUA service reset (stop -> clear cache -> start)...")
	fmt.Println("This requires Administrator privileges.")

	if err := patch.ResetForTesting(); err != nil {
		fmt.Println("RESET FAILED:", err)
		return
	}
	fmt.Println("Reset completed successfully — wuauserv is running again.")
}