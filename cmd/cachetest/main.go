// cachetest is a dev/test CLI for the encrypted patch cache (internal/patchcache).
// It uses the same DPAPI-backed key provider as the agent, so it exercises the
// real at-rest encryption on a real patch file.
//
// Usage:
//
//	cachetest <secure-dir> put <id> <file> [staged|cached]
//	cachetest <secure-dir> get <id> <out-file>
//	cachetest <secure-dir> list
//	cachetest <secure-dir> enforce
//	cachetest <secure-dir> rm <id>
//
// The cache cap can be overridden with AEGIS_CACHE_MAX_BYTES (bytes).
package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/aegisoneuem/aegis_one_agent/internal/patchcache"
	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
)

func main() {
	if len(os.Args) < 3 {
		usage()
		return
	}
	secureDir := os.Args[1]
	cmd := os.Args[2]

	var maxBytes int64
	if v := os.Getenv("AEGIS_CACHE_MAX_BYTES"); v != "" {
		maxBytes, _ = strconv.ParseInt(v, 10, 64)
	}

	keys := secure.NewKeyProvider(secureDir)
	cacheDir := secureDir + string(os.PathSeparator) + "cache"
	c, err := patchcache.New(cacheDir, keys, maxBytes, 0)
	if err != nil {
		fmt.Println("open cache:", err)
		os.Exit(1)
	}

	switch cmd {
	case "put":
		if len(os.Args) < 5 {
			usage()
			return
		}
		id, file := os.Args[3], os.Args[4]
		state := patchcache.StateCached
		if len(os.Args) > 5 && os.Args[5] == "staged" {
			state = patchcache.StateStaged
		}
		if c.Has(id) {
			fmt.Printf("note: %q is already cached — replacing it\n", id)
		}
		if err := c.Put(id, file, state); err != nil {
			fmt.Println("put failed:", err)
			os.Exit(1)
		}
		fmt.Printf("cached %q (state=%s) — encrypted under %s\n", id, state, cacheDir)

	case "get":
		if len(os.Args) < 5 {
			usage()
			return
		}
		id, out := os.Args[3], os.Args[4]
		if err := c.Decrypt(id, out); err != nil {
			fmt.Println("get failed:", err)
			os.Exit(1)
		}
		fmt.Printf("decrypted %q -> %s\n", id, out)

	case "list":
		ids := c.List()
		fmt.Printf("%d entries in %s:\n", len(ids), cacheDir)
		for _, e := range ids {
			fmt.Printf("  id=%s state=%s size=%d last_access=%d\n", e.ID, e.State, e.Size, e.LastAccess)
		}

	case "enforce":
		if err := c.Enforce(); err != nil {
			fmt.Println("enforce:", err)
			os.Exit(1)
		}
		fmt.Println("enforce complete")

	case "rm":
		if len(os.Args) < 4 {
			usage()
			return
		}
		if err := c.Remove(os.Args[3]); err != nil {
			fmt.Println("rm failed:", err)
			os.Exit(1)
		}
		fmt.Println("removed", os.Args[3])

	default:
		usage()
	}
}

func usage() {
	fmt.Print(`Usage:
  cachetest <secure-dir> put <id> <file> [staged|cached]
  cachetest <secure-dir> get <id> <out-file>
  cachetest <secure-dir> list
  cachetest <secure-dir> enforce
  cachetest <secure-dir> rm <id>
`)
}
