package secure

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// Chunked streaming AEAD for large files (patch packages up to several GB, and
// future OS images). The whole file is never held in memory: it is split into
// fixed-size plaintext chunks, each sealed independently with AES-256-GCM.
//
// On-disk format:
//
//	header: "AEG1" (4) | version (1) | reserved (3) | salt (8) | chunkSize (4, BE)
//	chunks: repeated [ ciphertextLen (4, BE) | ciphertext+tag ]
//
// Each chunk's 12-byte nonce is salt(8)||counter(4), so nonces are unique within
// a file (random salt) and across chunks (counter) — no reuse under one key.
// The counter in the nonce makes chunk reordering fail authentication. A 1-byte
// "final" flag is passed as GCM additional data (0 = more follow, 1 = last), so
// dropping the final chunk (truncation) is detected: the new last chunk was
// sealed with final=0 but is opened as final=1, which fails the auth check.
const (
	fcMagic          = "AEG1"
	fcVersion        = 1
	fcSaltSize       = 8
	fcHeaderSize     = 4 + 1 + 3 + fcSaltSize + 4
	defaultChunkSize = 1 << 20 // 1 MiB plaintext per chunk
)

func chunkNonce(salt []byte, counter uint32) []byte {
	n := make([]byte, 12)
	copy(n[:fcSaltSize], salt)
	binary.BigEndian.PutUint32(n[fcSaltSize:], counter)
	return n
}

func finalByte(last bool) []byte {
	if last {
		return []byte{1}
	}
	return []byte{0}
}

// EncryptFile encrypts srcPath into dstPath using chunked AES-256-GCM. On any
// error the partial destination is removed so no half-written file is left.
func EncryptFile(key []byte, srcPath, dstPath string) (err error) {
	gcm, err := newGCM(key)
	if err != nil {
		return err
	}
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open source %q: %w", srcPath, err)
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("create dest %q: %w", dstPath, err)
	}
	defer func() {
		dst.Close()
		if err != nil {
			os.Remove(dstPath)
		}
	}()

	salt := make([]byte, fcSaltSize)
	if _, err = io.ReadFull(rand.Reader, salt); err != nil {
		return fmt.Errorf("generate salt: %w", err)
	}

	w := bufio.NewWriter(dst)
	header := make([]byte, 0, fcHeaderSize)
	header = append(header, fcMagic...)
	header = append(header, fcVersion, 0, 0, 0)
	header = append(header, salt...)
	var csz [4]byte
	binary.BigEndian.PutUint32(csz[:], uint32(defaultChunkSize))
	header = append(header, csz[:]...)
	if _, err = w.Write(header); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	r := bufio.NewReaderSize(src, defaultChunkSize)
	var counter uint32
	for {
		chunk := make([]byte, defaultChunkSize)
		n, readErr := io.ReadFull(r, chunk)
		chunk = chunk[:n]
		atEOF := errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF)
		if readErr != nil && !atEOF {
			return fmt.Errorf("read source: %w", readErr)
		}
		last := atEOF
		if !last {
			if _, peekErr := r.Peek(1); peekErr != nil {
				if errors.Is(peekErr, io.EOF) {
					last = true
				} else {
					return fmt.Errorf("peek source: %w", peekErr)
				}
			}
		}

		ct := gcm.Seal(nil, chunkNonce(salt, counter), chunk, finalByte(last))
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(ct)))
		if _, err = w.Write(lenBuf[:]); err != nil {
			return fmt.Errorf("write chunk length: %w", err)
		}
		if _, err = w.Write(ct); err != nil {
			return fmt.Errorf("write chunk: %w", err)
		}
		counter++
		if last {
			break
		}
	}
	if err = w.Flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	return nil
}

// DecryptFile reverses EncryptFile. It fails loud on truncation, reordering, or
// tampering (GCM authentication), and removes the partial plaintext on error so
// a corrupt/partial file is never handed on for install.
func DecryptFile(key []byte, srcPath, dstPath string) (err error) {
	gcm, err := newGCM(key)
	if err != nil {
		return err
	}
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open source %q: %w", srcPath, err)
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("create dest %q: %w", dstPath, err)
	}
	defer func() {
		dst.Close()
		if err != nil {
			os.Remove(dstPath)
		}
	}()

	br := bufio.NewReader(src)
	header := make([]byte, fcHeaderSize)
	if _, err = io.ReadFull(br, header); err != nil {
		return fmt.Errorf("read header (too short?): %w", err)
	}
	if string(header[:4]) != fcMagic {
		return errors.New("bad magic: not an AegisOne encrypted file")
	}
	if header[4] != fcVersion {
		return fmt.Errorf("unsupported format version %d", header[4])
	}
	salt := header[8 : 8+fcSaltSize]
	chunkSize := binary.BigEndian.Uint32(header[8+fcSaltSize:])
	maxCipher := int(chunkSize) + 64 // plaintext chunk + GCM tag + slack

	w := bufio.NewWriter(dst)
	var counter uint32
	for {
		var lenBuf [4]byte
		if _, e := io.ReadFull(br, lenBuf[:]); e != nil {
			if errors.Is(e, io.EOF) {
				// Clean EOF at a chunk boundary: the previous chunk should have
				// been the final one and broken the loop. Reaching here means the
				// final chunk was dropped.
				return errors.New("truncated: missing final chunk")
			}
			return fmt.Errorf("read chunk length: %w", e)
		}
		clen := binary.BigEndian.Uint32(lenBuf[:])
		if clen == 0 || int(clen) > maxCipher {
			return fmt.Errorf("invalid chunk length %d", clen)
		}
		ct := make([]byte, clen)
		if _, e := io.ReadFull(br, ct); e != nil {
			return fmt.Errorf("truncated chunk %d: %w", counter, e)
		}

		_, peekErr := br.Peek(1)
		last := errors.Is(peekErr, io.EOF)
		if peekErr != nil && !last {
			return fmt.Errorf("peek: %w", peekErr)
		}

		pt, e := gcm.Open(nil, chunkNonce(salt, counter), ct, finalByte(last))
		if e != nil {
			return fmt.Errorf("decrypt/authenticate failed at chunk %d (tampered/reordered/truncated or wrong key): %w", counter, e)
		}
		if _, err = w.Write(pt); err != nil {
			return fmt.Errorf("write plaintext: %w", err)
		}
		counter++
		if last {
			break
		}
	}
	if err = w.Flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	return nil
}
