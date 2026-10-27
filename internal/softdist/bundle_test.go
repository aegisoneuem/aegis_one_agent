package softdist

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func bundleFixture(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	source := filepath.Join(base, "source")
	if err := os.MkdirAll(filepath.Join(source, "settings"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"setup.exe": "installer bytes", "settings/config.ini": "REGION=apac"} {
		if err := os.WriteFile(filepath.Join(source, filepath.FromSlash(name)), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(base, "app.zip")
	sum, err := PackBundle(context.Background(), source, archive, BundleLimits{})
	if err != nil {
		t.Fatal(err)
	}
	return archive, sum
}
func TestBundlePrepareLifecycle(t *testing.T) {
	archive, sum := bundleFixture(t)
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "managed cleanup", true: "custom retention"}[custom], func(t *testing.T) {
			base := t.TempDir()
			o := BundleOptions{EntryPoint: "setup.exe", InstallerType: "exe", ManagedRoot: filepath.Join(base, "packages")}
			if custom {
				o.Destination = filepath.Join(base, "retained")
			}
			b, err := PrepareBundle(context.Background(), archive, sum, o)
			if err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(filepath.Join(b.Directory, "settings", "config.ini")); err != nil || string(data) != "REGION=apac" {
				t.Fatal("support file missing")
			}
			if b.EntryHash == "" || filepath.Base(b.EntryPath) != "setup.exe" {
				t.Fatal("entry identity missing")
			}
			state, err := b.Finalize()
			if err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Stat(b.Directory)
			if custom {
				if state != "retained" || statErr != nil {
					t.Fatal("custom bundle not retained")
				}
			} else if state != "removed" || !os.IsNotExist(statErr) {
				t.Fatal("managed tree not removed")
			}
			if second, err := b.Finalize(); err != nil || second != state {
				t.Fatal("finalization not idempotent")
			}
		})
	}
}
func TestBundleIntegrityAndDestinationRefusal(t *testing.T) {
	archive, sum := bundleFixture(t)
	base := t.TempDir()
	o := BundleOptions{EntryPoint: "setup.exe", ManagedRoot: filepath.Join(base, "packages")}
	if _, err := PrepareBundle(context.Background(), archive, hex.EncodeToString(make([]byte, 32)), o); err == nil {
		t.Fatal("bad archive hash accepted")
	}
	if _, err := os.Stat(o.ManagedRoot); !os.IsNotExist(err) {
		t.Fatal("created extraction tree before ZIP integrity passed")
	}
	o.Destination = base
	if _, err := PrepareBundle(context.Background(), archive, sum, o); err == nil {
		t.Fatal("existing custom folder accepted")
	}
	o.Destination = filepath.Join(base, "fresh")
	o.Cleanup = "always"
	b, err := PrepareBundle(context.Background(), archive, sum, o)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := b.Finalize(); err != nil || state != "removed" {
		t.Fatal("explicit custom cleanup failed")
	}
}
func TestBundleManifestPathRules(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "C:/escape", `a\b`, "NUL.txt", "con/child", "a.", "a /child", "a//b", BundleManifestName, "a\x00b"} {
		if _, err := bundleName(name); err == nil {
			t.Errorf("unsafe path accepted: %q", name)
		}
	}
	h := hex.EncodeToString(make([]byte, 32))
	for _, files := range [][]BundleFile{
		{{Path: "a", SHA256: h}, {Path: "A", SHA256: h}},
		{{Path: "a", SHA256: h}, {Path: "a/b", SHA256: h}},
		{{Path: "a", Size: -1, SHA256: h}},
		{{Path: "a", Size: 11, SHA256: h}},
	} {
		if validateBundleManifest(BundleManifest{Version: 1, Files: files}, BundleLimits{MaxBytes: 10, MaxFiles: 10}) == nil {
			t.Fatal("invalid manifest accepted")
		}
	}
}
func TestBundleMemberTamperingAndLinks(t *testing.T) {
	for _, link := range []bool{false, true} {
		base := t.TempDir()
		archive := filepath.Join(base, "bad.zip")
		f, err := os.Create(archive)
		if err != nil {
			t.Fatal(err)
		}
		z := zip.NewWriter(f)
		h := sha256.Sum256([]byte("original"))
		m := BundleManifest{Version: 1, Files: []BundleFile{{Path: "setup.exe", Size: 8, SHA256: hex.EncodeToString(h[:])}}}
		raw, _ := json.Marshal(m)
		w, _ := z.Create(BundleManifestName)
		w.Write(raw)
		header := &zip.FileHeader{Name: "setup.exe", Method: zip.Store}
		if link {
			header.SetMode(os.ModeSymlink | 0777)
		}
		w, _ = z.CreateHeader(header)
		w.Write([]byte("tampered"))
		z.Close()
		f.Close()
		sum, _ := bundleHash(context.Background(), archive)
		root := filepath.Join(base, "packages")
		if _, err = PrepareBundle(context.Background(), archive, sum, BundleOptions{EntryPoint: "setup.exe", ManagedRoot: root}); err == nil {
			t.Fatal("tampered/link member accepted")
		}
		entries, _ := os.ReadDir(root)
		if len(entries) != 0 {
			t.Fatal("failed bundle left plaintext extraction")
		}
	}
}
func TestBundlePackAndEntryLimits(t *testing.T) {
	archive, sum := bundleFixture(t)
	base := filepath.Dir(archive)
	source := filepath.Join(base, "source")
	if _, err := PackBundle(context.Background(), source, filepath.Join(source, "inside.zip"), BundleLimits{}); err == nil {
		t.Fatal("self-including output allowed")
	}
	if _, err := PackBundle(context.Background(), source, filepath.Join(base, "too-small.zip"), BundleLimits{MaxBytes: 2}); err == nil {
		t.Fatal("byte limit ignored")
	}
	o := BundleOptions{EntryPoint: "absent.exe", ManagedRoot: filepath.Join(base, "packages")}
	if _, err := PrepareBundle(context.Background(), archive, sum, o); err == nil {
		t.Fatal("unmanifested entry point accepted")
	}
	o.EntryPoint = "setup.exe"
	o.Cleanup = "retain"
	if o.Validate() == nil {
		t.Fatal("retention without custom path accepted")
	}
	o = BundleOptions{InstallerType: "command", Command: "cmd.exe", ManagedRoot: filepath.Join(base, "packages")}
	b, err := PrepareBundle(context.Background(), archive, sum, o)
	if err != nil {
		t.Fatal(err)
	}
	if b.EntryPath != "" {
		t.Fatal("external command confused with installer entry")
	}
	b.Finalize()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = PrepareBundle(ctx, archive, sum, o); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestBundleDirectoryBoundsBeforeExtraction(t *testing.T) {
	archive, _ := bundleFixture(t)
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	end := len(raw) - 22
	binary.LittleEndian.PutUint16(raw[end+8:end+10], 1)
	binary.LittleEndian.PutUint16(raw[end+10:end+12], 1)
	altered := filepath.Join(t.TempDir(), "misreported.zip")
	os.WriteFile(altered, raw, 0600)
	sum, _ := bundleHash(context.Background(), altered)
	root := filepath.Join(t.TempDir(), "packages")
	if _, err := PrepareBundle(context.Background(), altered, sum, BundleOptions{EntryPoint: "setup.exe", ManagedRoot: root}); err == nil {
		t.Fatal("understated central-directory count accepted")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("extracted before directory bounds passed")
	}
}

func TestBundleZIP64Directory(t *testing.T) {
	archive, _ := bundleFixture(t)
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	endOffset := len(raw) - 22
	end := append([]byte(nil), raw[endOffset:]...)
	count := uint64(binary.LittleEndian.Uint16(end[10:12]))
	size := uint64(binary.LittleEndian.Uint32(end[12:16]))
	offset := uint64(binary.LittleEndian.Uint32(end[16:20]))
	record := make([]byte, 56)
	binary.LittleEndian.PutUint32(record[:4], 0x06064b50)
	binary.LittleEndian.PutUint64(record[4:12], 44)
	binary.LittleEndian.PutUint16(record[12:14], 45)
	binary.LittleEndian.PutUint16(record[14:16], 45)
	binary.LittleEndian.PutUint64(record[24:32], count)
	binary.LittleEndian.PutUint64(record[32:40], count)
	binary.LittleEndian.PutUint64(record[40:48], size)
	binary.LittleEndian.PutUint64(record[48:56], offset)
	locator := make([]byte, 20)
	binary.LittleEndian.PutUint32(locator[:4], 0x07064b50)
	binary.LittleEndian.PutUint64(locator[8:16], uint64(endOffset))
	binary.LittleEndian.PutUint32(locator[16:20], 1)
	binary.LittleEndian.PutUint32(end[16:20], 0xffffffff)
	converted := append(append(append(append([]byte(nil), raw[:endOffset]...), record...), locator...), end...)
	path := filepath.Join(t.TempDir(), "zip64.zip")
	os.WriteFile(path, converted, 0600)
	sum, _ := bundleHash(context.Background(), path)
	b, err := PrepareBundle(context.Background(), path, sum, BundleOptions{EntryPoint: "setup.exe", ManagedRoot: filepath.Join(t.TempDir(), "packages")})
	if err != nil {
		t.Fatal(err)
	}
	if state, err := b.Finalize(); err != nil || state != "removed" {
		t.Fatal("ZIP64 cleanup failed")
	}
}
