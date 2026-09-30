package buffer

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
)

// FileBuffer is a file-backed FIFO queue storing one JSON record per line. When
// constructed with a KeyProvider it encrypts each record's payload at rest with
// AES-256-GCM (the "Local Secure Buffer"); without one it stores payloads in
// plaintext (dev/test only).
//
// Only the payload is encrypted — the "kind" tag is non-sensitive routing
// metadata and stays clear so the queue stays scannable.
type FileBuffer struct {
	path string
	mu   sync.Mutex
	keys secure.KeyProvider // nil => plaintext (dev/test)
}

type record struct {
	Kind    string `json:"kind"`
	Payload string `json:"payload"` // base64 of the (optionally encrypted) payload
}

// NewFileBuffer returns a plaintext FileBuffer backed by the file at path,
// creating the parent directory if needed. For production use
// NewEncryptedFileBuffer so data is protected at rest.
func NewFileBuffer(path string) (*FileBuffer, error) {
	return newFileBuffer(path, nil)
}

// NewEncryptedFileBuffer returns a FileBuffer that seals each payload with
// AES-256-GCM under the key from keys before writing it to disk.
func NewEncryptedFileBuffer(path string, keys secure.KeyProvider) (*FileBuffer, error) {
	if keys == nil {
		return nil, fmt.Errorf("encrypted buffer requires a key provider")
	}
	return newFileBuffer(path, keys)
}

func newFileBuffer(path string, keys secure.KeyProvider) (*FileBuffer, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create buffer dir %q: %w", dir, err)
		}
	}
	return &FileBuffer{path: path, keys: keys}, nil
}

// encode turns a raw payload into the on-disk string form (encrypt if keyed,
// then base64). decode reverses it.
func (b *FileBuffer) encode(payload []byte) (string, error) {
	stored := payload
	if b.keys != nil {
		key, err := b.keys.Key()
		if err != nil {
			return "", fmt.Errorf("get encryption key: %w", err)
		}
		sealed, err := secure.Seal(key, payload)
		if err != nil {
			return "", fmt.Errorf("encrypt payload: %w", err)
		}
		stored = sealed
	}
	return base64.StdEncoding.EncodeToString(stored), nil
}

func (b *FileBuffer) decode(s string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if b.keys == nil {
		return raw, nil
	}
	key, err := b.keys.Key()
	if err != nil {
		return nil, fmt.Errorf("get encryption key: %w", err)
	}
	return secure.Open(key, raw)
}

func (b *FileBuffer) Enqueue(kind string, payload []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	f, err := os.OpenFile(b.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open buffer: %w", err)
	}
	defer f.Close()

	encoded, err := b.encode(payload)
	if err != nil {
		return err
	}
	line, err := json.Marshal(record{Kind: kind, Payload: encoded})
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
		payload, err := b.decode(r.Payload)
		if err != nil {
			// Corrupt or (for an encrypted buffer) tampered/undecryptable record.
			// Fail loud so tampering is visible, but drop it rather than blocking
			// the queue forever behind one bad record.
			slog.Warn("buffer: dropping undecodable record", "kind", r.Kind, "error", err.Error())
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
