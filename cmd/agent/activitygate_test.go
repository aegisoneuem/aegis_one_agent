package main

import (
	"sync"
	"testing"
)

func TestActivityGate_BasicEnterLeave(t *testing.T) {
	var g activityGate
	if g.Busy() {
		t.Fatal("zero-value gate must not be busy")
	}
	g.Enter()
	if !g.Busy() {
		t.Fatal("gate must be busy after Enter")
	}
	g.Leave()
	if g.Busy() {
		t.Fatal("gate must be free after matching Leave")
	}
}

func TestActivityGate_NestedComposes(t *testing.T) {
	var g activityGate
	g.Enter()
	g.Enter()
	g.Leave()
	if !g.Busy() {
		t.Fatal("gate must stay busy until the LAST op leaves")
	}
	g.Leave()
	if g.Busy() {
		t.Fatal("gate must be free after all ops leave")
	}
}

// A stray Leave (unpaired) must not drive the counter negative and wedge the gate
// into a permanently "free" state while a real op is still running.
func TestActivityGate_StrayLeaveClampsAtZero(t *testing.T) {
	var g activityGate
	g.Leave() // unpaired
	if g.Busy() {
		t.Fatal("stray Leave must not make the gate busy or negative")
	}
	g.Enter()
	if !g.Busy() {
		t.Fatal("after clamp, a real Enter must register as busy")
	}
	g.Leave()
	if g.Busy() {
		t.Fatal("gate must be free again")
	}
}

func TestActivityGate_ConcurrentBalanced(t *testing.T) {
	var g activityGate
	const workers = 50
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			g.Enter()
			g.Leave()
		}()
	}
	wg.Wait()
	if g.Busy() {
		t.Fatal("after balanced concurrent Enter/Leave the gate must be free")
	}
}
