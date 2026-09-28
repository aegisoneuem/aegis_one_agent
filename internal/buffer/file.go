package buffer

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// FileBuffer is a simple file-backed queue used until the encrypted-SQLite Local
// Secure Buffer exists. It stores one JSON record per line.
//
// It is NOT encrypted — a deliberate placeholder that satisfies the Buffer
// contract so offline routing can be built and tested now. Do not queue secrets
// through it; patch state (build + KB numbers) is not sensitive. The
// encrypted-SQLite implementation will replace this behind the same interface.
type FileBuffer struct {
	path string
	mu   sync.Mutex
}

type record struct {
	Kind    string `json:"kind"`
	Payload string `json:"payload"` // base64 of the raw payload
}

// NewFileBuffer returns a FileBuffer backed by the file at path, creating the
// parent directory if needed.
func NewFileBuffer(path string) (*FileBuffer, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create buffer dir %q: %w", dir, err)
		}
	}
	return &FileBuffer{path: path}, nil
}

func (b *FileBuffer) Enqueue(kind string, payload []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	f, err := os.OpenFile(b.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open buffer: %w", err)
	}
	defer f.Close()

	line, err := json.Marshal(record{Kind: kind, Payload: base64.StdEncoding.EncodeToString(payload)})
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("append to buffer: %w", err)
	}
	return nil
}

func (b *FileBuffer) Drain(handle func(kind string, payload []byte) error) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	records, err := b.readAll()
	if err != nil {
		return 0, err
	}

	drained := 0
	for i, r := range records {
		payload, err := base64.StdEncoding.DecodeString(r.Payload)
		if err != nil {
			// Corrupt line — drop it rather than blocking the queue forever.
			drained++
			continue
		}
		if err := handle(r.Kind, payload); err != nil {
			// Keep this and every following record for a later retry.
			if writeErr := b.writeAll(records[i:]); writeErr != nil {
				return drained, writeErr
			}
			return drained, nil
		}
		drained++
	}

	// Everything handled — clear the file.
	if err := b.writeAll(nil); err != nil {
		return drained, err
	}
	return drained, nil
}

func (b *FileBuffer) Len() (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	records, err := b.readAll()
	if err != nil {
		return 0, err
	}
	return len(records), nil
}

func (b *FileBuffer) readAll() ([]record, error) {
	f, err := os.Open(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open buffer: %w", err)
	}
	defer f.Close()

	var out []record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r record
		if err := json.Unmarshal(line, &r); err != nil {
			continue // skip corrupt line
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

func (b *FileBuffer) writeAll(records []record) error {
	if len(records) == 0 {
		// Truncate to empty (or remove) so a drained queue takes no space.
		return os.WriteFile(b.path, nil, 0o644)
	}
	tmp := b.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, r := range records {
		line, err := json.Marshal(r)
		if err != nil {
			f.Close()
			return err
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, b.path) // atomic replace
}
