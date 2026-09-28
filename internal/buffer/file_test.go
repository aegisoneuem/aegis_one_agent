package buffer

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestFileBufferEnqueueDrainFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "buf.jsonl")
	b, err := NewFileBuffer(path)
	if err != nil {
		t.Fatal(err)
	}

	if n, _ := b.Len(); n != 0 {
		t.Fatalf("new buffer Len=%d, want 0", n)
	}
	if err := b.Enqueue("k", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := b.Enqueue("k", []byte("two")); err != nil {
		t.Fatal(err)
	}
	if n, _ := b.Len(); n != 2 {
		t.Fatalf("Len=%d, want 2", n)
	}

	var got []string
	n, err := b.Drain(func(kind string, payload []byte) error {
		got = append(got, string(payload))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("drained=%d, want 2", n)
	}
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("FIFO order wrong: %v", got)
	}
	if n, _ := b.Len(); n != 0 {
		t.Fatalf("after drain Len=%d, want 0", n)
	}
}

func TestFileBufferDrainKeepsOnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "buf.jsonl")
	b, _ := NewFileBuffer(path)
	for _, s := range []string{"a", "b", "c"} {
		if err := b.Enqueue("k", []byte(s)); err != nil {
			t.Fatal(err)
		}
	}

	n, err := b.Drain(func(kind string, payload []byte) error {
		if string(payload) == "b" {
			return errors.New("stop here")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("drained=%d, want 1 (only 'a')", n)
	}
	if l, _ := b.Len(); l != 2 {
		t.Fatalf("remaining Len=%d, want 2 ('b','c' kept)", l)
	}
}

func TestFileBufferPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "buf.jsonl")
	b1, _ := NewFileBuffer(path)
	_ = b1.Enqueue("k", []byte("persisted"))

	b2, _ := NewFileBuffer(path)
	if n, _ := b2.Len(); n != 1 {
		t.Fatalf("reopened Len=%d, want 1", n)
	}
}
