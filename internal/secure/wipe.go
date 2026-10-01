package secure

import (
	"crypto/rand"
	"os"
)

// SecureDelete best-effort removes a plaintext file: it overwrites the file's
// bytes with random data, syncs, then unlinks it.
//
// HONEST LIMITATION: on SSDs and copy-on-write filesystems, wear-leveling means
// an in-place overwrite does not guarantee the original blocks are erased. This
// lowers casual recoverability of a transient plaintext temp; it is NOT a
// forensic wipe. Real at-rest protection relies on full-disk encryption
// (e.g. BitLocker). A missing file is treated as already deleted.
func SecureDelete(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	if info.Mode().IsRegular() && info.Size() > 0 {
		if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
			remaining := info.Size()
			buf := make([]byte, 64*1024)
			for remaining > 0 {
				n := int64(len(buf))
				if n > remaining {
					n = remaining
				}
				if _, rerr := rand.Read(buf[:n]); rerr != nil {
					break
				}
				if _, werr := f.Write(buf[:n]); werr != nil {
					break
				}
				remaining -= n
			}
			_ = f.Sync()
			_ = f.Close()
		}
	}
	return os.Remove(path)
}
