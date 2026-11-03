package fileops

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestRecursiveBinaryTransferAndOverwrite(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	data := bytes.Repeat([]byte{0, 255, 1, '\r', '\n'}, 250000)
	writeTestFile(t, filepath.Join(source, "nested", "data.bin"), data)
	if err := os.MkdirAll(filepath.Join(source, "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "bundle.zip")
	spec := Spec{Source: source, Recursive: true}
	packed, err := Pack(context.Background(), spec, archive)
	if err != nil {
		t.Fatal(err)
	}
	spec.Entries = packed.Entries
	spec.Destination = filepath.Join(root, "destination")
	spec.DestinationDir = true
	result, err := Unpack(context.Background(), spec, archive)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(spec.Destination, "source", "nested", "data.bin"))
	if err != nil || !bytes.Equal(got, data) || result.Files != 1 {
		t.Fatalf("wrong binary copy: %d bytes %v", len(got), err)
	}
	if info, err := os.Stat(filepath.Join(spec.Destination, "source", "empty")); err != nil || !info.IsDir() {
		t.Fatal("empty directory not preserved")
	}
	if _, err = Unpack(context.Background(), spec, archive); err == nil {
		t.Fatal("overwrote existing file without flag")
	}
	spec.Overwrite = true
	if _, err = Unpack(context.Background(), spec, archive); err != nil {
		t.Fatal(err)
	}
}
func TestGlobRenameLimitsAndIntegrity(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	writeTestFile(t, source, []byte("exact data"))
	archive := filepath.Join(root, "bundle.zip")
	spec := Spec{Source: filepath.Join(root, "*.txt")}
	packed, err := Pack(context.Background(), spec, archive)
	if err != nil {
		t.Fatal(err)
	}
	spec.Entries = packed.Entries
	spec.Destination = filepath.Join(root, "renamed.txt")
	if _, err = Unpack(context.Background(), spec, archive); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(spec.Destination); string(got) != "exact data" {
		t.Fatal("rename altered bytes")
	}
	spec.Destination = filepath.Join(root, "bad.txt")
	spec.Entries[0].SHA256 = "bad"
	if _, err = Unpack(context.Background(), spec, archive); err == nil {
		t.Fatal("invalid file hash accepted")
	}
	if _, err = os.Stat(spec.Destination); !os.IsNotExist(err) {
		t.Fatal("published unverified file")
	}
	if _, err = Pack(context.Background(), Spec{Source: source, MaxBytes: 2}, filepath.Join(root, "small.zip")); err == nil {
		t.Fatal("ignored byte cap")
	}
}
func TestMoveRefusesChangedSourcesBeforeDeletingAny(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a.txt"), filepath.Join(root, "b.txt")
	writeTestFile(t, a, []byte("a"))
	writeTestFile(t, b, []byte("b"))
	packed, err := Pack(context.Background(), Spec{Source: filepath.Join(root, "*.txt")}, filepath.Join(root, "bundle.zip"))
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, b, []byte("changed"))
	if _, err = DeleteManifest(context.Background(), packed.Entries); err == nil {
		t.Fatal("deleted changed source")
	}
	if _, err = os.Stat(a); err != nil {
		t.Fatal("deleted earlier file before checking all hashes")
	}
}
func TestArchiveTraversalAndRecursiveDelete(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "malicious.zip")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	w, _ := z.Create("../outside")
	w.Write([]byte("bad"))
	z.Close()
	f.Close()
	_, err = Unpack(context.Background(), Spec{Destination: filepath.Join(root, "dest"), Entries: []Entry{{Name: "../outside", Size: 3}}}, archive)
	if err == nil {
		t.Fatal("accepted path traversal")
	}
	dir := filepath.Join(root, "remove-me")
	writeTestFile(t, filepath.Join(dir, "child.txt"), []byte("data"))
	if _, err = Basic(context.Background(), Spec{Operation: "delete", Source: dir}); err == nil {
		t.Fatal("deleted directory without recursive flag")
	}
	if _, err = Basic(context.Background(), Spec{Operation: "delete", Source: dir, Recursive: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("directory still exists")
	}
}
func TestSamePathAndCancelledTasks(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "a.txt")
	writeTestFile(t, source, []byte("a"))
	spec := Spec{Destination: source, Entries: []Entry{{Source: source, Name: "a.txt"}}}
	if err := ValidateLocalCopy(spec); err == nil {
		t.Fatal("allowed move/copy to self")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Pack(ctx, Spec{Source: source}, filepath.Join(root, "cancelled.zip")); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestWriteBytes_Overwrite(t *testing.T) {
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "sub", "out.bin")
	spec := Spec{
		Version:     1,
		Operation:   "write_bytes",
		Destination: dst,
		Content:     []byte("hello world"),
	}
	res, err := Basic(context.Background(), spec)
	if err != nil {
		t.Fatalf("write_bytes failed: %v", err)
	}
	if res.Bytes != 11 || res.Files != 1 {
		t.Errorf("result = %+v, want Files=1 Bytes=11", res)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello world" {
		t.Errorf("file content = %q, want %q", got, "hello world")
	}
	// Overwrite with shorter content.
	spec.Content = []byte("hi")
	if _, err := Basic(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(dst)
	if string(got) != "hi" {
		t.Errorf("overwrite content = %q, want %q", got, "hi")
	}
}

func TestWriteBytes_Append(t *testing.T) {
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "log.txt")
	spec := Spec{Version: 1, Operation: "write_bytes", Destination: dst, Content: []byte("line1\n")}
	if _, err := Basic(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	spec.Content = []byte("line2\n")
	spec.Append = true
	if _, err := Basic(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "line1\nline2\n" {
		t.Errorf("append result = %q, want %q", got, "line1\nline2\n")
	}
}

func TestWriteBytes_MissingDestination(t *testing.T) {
	spec := Spec{Version: 1, Operation: "write_bytes", Content: []byte("x")}
	if _, err := Basic(context.Background(), spec); err == nil {
		t.Error("empty destination accepted")
	}
}

func TestWriteBytes_ContentAndContentIDMutuallyExclusive(t *testing.T) {
	spec := Spec{
		Version: 1, Operation: "write_bytes",
		Destination: "ignored",
		Content:     []byte("x"),
		ContentID:   "some-id",
	}
	if _, err := Basic(context.Background(), spec); err == nil {
		t.Error("content + content_id combination accepted")
	}
}

func TestWriteBytes_SHA256Verification(t *testing.T) {
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "sum.bin")
	payload := []byte("checksum me")
	// Correct SHA.
	h := sha256.Sum256(payload)
	spec := Spec{
		Version: 1, Operation: "write_bytes",
		Destination: dst, Content: payload,
		SHA256: hex.EncodeToString(h[:]),
	}
	if _, err := Basic(context.Background(), spec); err != nil {
		t.Fatalf("correct SHA rejected: %v", err)
	}
	// Wrong SHA.
	spec.SHA256 = "deadbeef"
	if _, err := Basic(context.Background(), spec); err == nil {
		t.Error("wrong SHA accepted")
	}
}
