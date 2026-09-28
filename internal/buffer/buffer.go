// Package buffer is the agent's offline queue ("Local Secure Buffer"). When the
// agent cannot reach the server, data (patch state, command results, events) is
// enqueued here and drained on reconnect.
//
// The production implementation will be an encrypted (AES-256-GCM) SQLite queue
// per the Agent Architecture Spec. This interface lets callers depend on the
// contract now; the encrypted-SQLite implementation is a separate task and can
// replace the current file-backed stub without changing callers.
package buffer

// Buffer is a durable FIFO queue of opaque payloads tagged by kind.
type Buffer interface {
	// Enqueue appends one payload under a caller-defined kind (e.g. "patch_state").
	Enqueue(kind string, payload []byte) error

	// Drain hands each queued item to handle in FIFO order. Items for which
	// handle returns nil are removed; an item whose handle returns an error is
	// kept (to retry later) and stops the drain. Returns the count removed.
	Drain(handle func(kind string, payload []byte) error) (int, error)

	// Len reports how many items are currently queued.
	Len() (int, error)
}
