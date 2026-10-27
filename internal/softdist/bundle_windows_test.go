//go:build windows

package softdist

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBundleCleanupNeverFollowsJunction(t *testing.T) {
	archive, sum := bundleFixture(t)
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	os.Mkdir(outside, 0700)
	keep := filepath.Join(outside, "keep.txt")
	os.WriteFile(keep, []byte("must survive"), 0600)
	b, err := PrepareBundle(context.Background(), archive, sum, BundleOptions{EntryPoint: "setup.exe", ManagedRoot: filepath.Join(base, "packages")})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Finalize()
	junction := filepath.Join(b.Directory, "external")
	if out, err := exec.Command("cmd.exe", "/D", "/C", "mklink", "/J", junction, outside).CombinedOutput(); err != nil {
		t.Skipf("junction unavailable: %v (%s)", err, out)
	}
	_, cleanupErr := b.Finalize()
	if data, err := os.ReadFile(keep); err != nil || string(data) != "must survive" {
		t.Fatal("bundle cleanup traversed outside its owned tree")
	}
	if cleanupErr != nil {
		t.Logf("cleanup refused junction safely: %v", cleanupErr)
	}
}

func TestBundleArchiveReadLock(t *testing.T) {
	archive, _ := bundleFixture(t)
	locked, err := openBundleArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	writer, err := os.OpenFile(archive, os.O_WRONLY|os.O_TRUNC, 0600)
	if err == nil {
		writer.Close()
		t.Fatal("archive source remained writable during verification/extraction")
	}
}
