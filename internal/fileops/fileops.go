// Package fileops implements native filesystem tasks. It never invokes a shell.
package fileops

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const DefaultMaxBytes int64 = 500 << 20
const DefaultMaxFiles = 10000
const MaxManifestBytes = 2 << 20 // fit within existing gRPC message limits

// Spec is the JSON payload of a signed file_operation command (version 1).
type Spec struct {
	Version        int     `json:"version"`
	Operation      string  `json:"operation"`
	Source         string  `json:"source,omitempty"`
	Destination    string  `json:"destination,omitempty"`
	WorkingDir     string  `json:"working_dir,omitempty"`
	Recursive      bool    `json:"recursive,omitempty"`
	Overwrite      bool    `json:"overwrite,omitempty"`
	DestinationDir bool    `json:"destination_dir,omitempty"`
	MaxBytes       int64   `json:"max_bytes,omitempty"`
	MaxFiles       int     `json:"max_files,omitempty"`
	TimeoutSeconds int     `json:"timeout_seconds,omitempty"`
	ContentID      string  `json:"content_id,omitempty"`
	SHA256         string  `json:"sha256,omitempty"`
	Entries        []Entry `json:"entries,omitempty"`
	// Content is the inline byte payload for operation="write_bytes". JSON
	// marshals []byte as base64 by convention, so a 100 MB payload becomes
	// ~133 MB on the wire — the agent / mock gRPC max-recv-msg-size is sized
	// for this. Empty + ContentID set means "stream from server by id".
	Content []byte `json:"content,omitempty"`
	// Append — only honored for operation="write_bytes". true = open the
	// destination with O_APPEND and write at the end; false = overwrite /
	// truncate (default).
	Append bool `json:"append,omitempty"`
}
type Entry struct {
	Source    string `json:"source"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256,omitempty"`
	Directory bool   `json:"directory,omitempty"`
}
type Result struct {
	Files      int     `json:"files"`
	Bytes      int64   `json:"bytes"`
	Entries    []Entry `json:"entries,omitempty"`
	StoredPath string  `json:"stored_path,omitempty"`
	SHA256     string  `json:"sha256,omitempty"`
}

func (s Spec) Limits() (int64, int) {
	b, n := s.MaxBytes, s.MaxFiles
	if b <= 0 {
		b = DefaultMaxBytes
	}
	if b == 1<<63-1 {
		b--
	} // reserve one byte for overflow/limit detection
	if n <= 0 {
		n = DefaultMaxFiles
	}
	return b, n
}
func LocalPath(cwd, path string) string {
	if !filepath.IsAbs(path) {
		return filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

// Refuse copying/moving onto the source itself, including recursive descendants.
func ValidateLocalCopy(spec Spec) error {
	dest := LocalPath(spec.WorkingDir, spec.Destination)
	directory := spec.DestinationDir || len(spec.Entries) > 1 || (len(spec.Entries) > 0 && spec.Entries[0].Directory)
	if info, err := os.Stat(dest); err == nil && info.IsDir() {
		directory = true
	}
	for _, e := range spec.Entries {
		target := dest
		if directory {
			target = filepath.Join(dest, filepath.FromSlash(e.Name))
		}
		if strings.EqualFold(filepath.Clean(target), filepath.Clean(e.Source)) {
			return fmt.Errorf("source and destination are the same: %s", target)
		}
		if e.Directory {
			rel, err := filepath.Rel(e.Source, target)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("destination is inside source: %s", target)
			}
		}
	}
	return nil
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func Hash(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, contextReader{ctx, f}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Pack keeps directory structure, empty directories, binary bytes and hashes.
// Links are refused rather than silently copying data outside the selected tree.
func Pack(ctx context.Context, spec Spec, archive string) (Result, error) {
	var out Result
	if strings.TrimSpace(spec.Source) == "" {
		return out, fmt.Errorf("source path required")
	}
	source := LocalPath(spec.WorkingDir, spec.Source)
	matches, err := filepath.Glob(source)
	if err != nil {
		return out, err
	}
	if len(matches) == 0 {
		return out, fmt.Errorf("no files matched %s", source)
	}
	maxBytes, maxFiles := spec.Limits()
	f, err := os.OpenFile(archive, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return out, err
	}
	defer f.Close()
	z := zip.NewWriter(f)
	defer z.Close()
	seen := map[string]bool{}
	for _, match := range matches {
		base := filepath.Dir(match)
		err = filepath.WalkDir(match, func(path string, d os.DirEntry, walkErr error) error {
			if filepath.Clean(path) == filepath.Clean(archive) {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("links are not supported: %s", path)
			}
			if d.IsDir() && !spec.Recursive {
				return fmt.Errorf("directory requires --recursive: %s", path)
			}
			if len(out.Entries) >= maxFiles {
				return fmt.Errorf("file-count limit %d exceeded", maxFiles)
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() && !info.IsDir() {
				return fmt.Errorf("unsupported file: %s", path)
			}
			rel, err := filepath.Rel(base, path)
			if err != nil {
				return err
			}
			name := filepath.ToSlash(rel)
			if seen[strings.ToLower(name)] {
				return fmt.Errorf("duplicate archive path %s", name)
			}
			seen[strings.ToLower(name)] = true
			e := Entry{Source: path, Name: name, Directory: d.IsDir()}
			header, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			header.Name = name
			header.Method = zip.Store
			if d.IsDir() {
				header.Name += "/"
			}
			w, err := z.CreateHeader(header)
			if err != nil {
				return err
			}
			if !d.IsDir() {
				if info.Size() > maxBytes-out.Bytes {
					return fmt.Errorf("byte limit %d exceeded", maxBytes)
				}
				r, err := os.Open(path)
				if err != nil {
					return err
				}
				h := sha256.New()
				n, copyErr := io.Copy(io.MultiWriter(w, h), io.LimitReader(contextReader{ctx, r}, maxBytes-out.Bytes+1))
				r.Close()
				if copyErr != nil {
					return copyErr
				}
				if n > maxBytes-out.Bytes {
					return fmt.Errorf("byte limit exceeded")
				}
				e.Size = n
				e.SHA256 = hex.EncodeToString(h.Sum(nil))
				out.Files++
				out.Bytes += n
			}
			out.Entries = append(out.Entries, e)
			return nil
		})
		if err != nil {
			return out, err
		}
	}
	if err = z.Close(); err != nil {
		return out, err
	}
	if err = f.Close(); err != nil {
		return out, err
	}
	out.SHA256, err = Hash(ctx, archive)
	return out, err
}

func safeName(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("absolute archive path")
	}
	for _, p := range strings.Split(name, "/") {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, "<>:\"|?*\x00") || strings.TrimRight(p, " .") != p {
			return "", fmt.Errorf("unsafe archive path %q", name)
		}
	}
	return filepath.FromSlash(name), nil
}
func noLinks(path string) error {
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination link refused: %s", p)
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return nil
}

// Unpack verifies the complete signed manifest before publishing any files.
// Existing files require explicit overwrite. Errors may leave directories or
// already-published files; callers must report partial failure, never auto-retry.
func Unpack(ctx context.Context, spec Spec, archive string) (Result, error) {
	var out Result
	if strings.TrimSpace(spec.Destination) == "" {
		return out, fmt.Errorf("destination path required")
	}
	maxBytes, maxFiles := spec.Limits()
	if len(spec.Entries) == 0 || len(spec.Entries) > maxFiles {
		return out, fmt.Errorf("invalid manifest size")
	}
	z, err := zip.OpenReader(archive)
	if err != nil {
		return out, err
	}
	defer z.Close()
	if len(z.File) != len(spec.Entries) {
		return out, fmt.Errorf("archive/manifest entry count differs")
	}
	dest := LocalPath(spec.WorkingDir, spec.Destination)
	isDir := spec.DestinationDir || len(spec.Entries) > 1 || spec.Entries[0].Directory
	if info, err := os.Stat(dest); err == nil && info.IsDir() {
		isDir = true
	}
	root := dest
	if !isDir {
		root = filepath.Dir(dest)
	}
	if err = noLinks(root); err != nil {
		return out, err
	}
	if err = os.MkdirAll(root, 0755); err != nil {
		return out, err
	}
	stage, err := os.MkdirTemp(root, ".aegis-copy-")
	if err != nil {
		return out, err
	}
	defer os.RemoveAll(stage)
	type staged struct {
		entry      Entry
		file, temp string
		modified   time.Time
	}
	var ready []staged
	seen := map[string]bool{}
	for i, e := range spec.Entries {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		rel, err := safeName(e.Name)
		if err != nil {
			return out, err
		}
		target := dest
		if isDir {
			target = filepath.Join(root, rel)
		}
		key := strings.ToLower(target)
		if seen[key] {
			return out, fmt.Errorf("duplicate destination %s", target)
		}
		seen[key] = true
		if err = noLinks(target); err != nil {
			return out, err
		}
		if info, err := os.Stat(target); err == nil {
			if info.IsDir() != e.Directory {
				return out, fmt.Errorf("destination type conflict: %s", target)
			}
			if !e.Directory && !spec.Overwrite {
				return out, fmt.Errorf("destination exists (use --overwrite): %s", target)
			}
		} else if !os.IsNotExist(err) {
			return out, err
		}
		zf := z.File[i]
		if strings.TrimSuffix(zf.Name, "/") != e.Name || zf.FileInfo().IsDir() != e.Directory {
			return out, fmt.Errorf("archive/manifest mismatch")
		}
		item := staged{entry: e, file: target, modified: zf.Modified}
		if !e.Directory {
			if e.Size < 0 || e.Size > maxBytes-out.Bytes || zf.UncompressedSize64 != uint64(e.Size) {
				return out, fmt.Errorf("archive exceeds manifest/byte limit")
			}
			r, err := zf.Open()
			if err != nil {
				return out, err
			}
			f, err := os.CreateTemp(stage, "file-")
			if err != nil {
				r.Close()
				return out, err
			}
			h := sha256.New()
			n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(contextReader{ctx, r}, e.Size+1))
			r.Close()
			closeErr := f.Close()
			if copyErr != nil {
				return out, copyErr
			}
			if closeErr != nil {
				return out, closeErr
			}
			if n != e.Size || hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
				return out, fmt.Errorf("file integrity failure: %s", e.Name)
			}
			item.temp = f.Name()
			out.Bytes += n
		}
		ready = append(ready, item)
	}
	for _, item := range ready {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		if item.entry.Directory {
			if err = os.MkdirAll(item.file, 0755); err != nil {
				return out, err
			}
			continue
		}
		if err = os.MkdirAll(filepath.Dir(item.file), 0755); err != nil {
			return out, err
		}
		if spec.Overwrite {
			err = os.Rename(item.temp, item.file)
		} else {
			var f *os.File
			f, err = os.OpenFile(item.file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err == nil {
				var r *os.File
				r, err = os.Open(item.temp)
				if err == nil {
					_, err = io.Copy(f, contextReader{ctx, r})
					r.Close()
				}
				closeErr := f.Close()
				if err == nil {
					err = closeErr
				}
				if err != nil {
					os.Remove(item.file)
				}
			}
		}
		if err != nil {
			return out, fmt.Errorf("published %d files before failure: %w", out.Files, err)
		}
		out.Files++
		_ = os.Chtimes(item.file, item.modified, item.modified)
	}
	return out, nil
}

// DeleteManifest is used only after successful copy for move. Recheck ALL source
// hashes before deleting ANY file; changed files and newly created files survive.
func DeleteManifest(ctx context.Context, entries []Entry) (Result, error) {
	var out Result
	if len(entries) == 0 {
		return out, fmt.Errorf("empty source manifest")
	}
	for _, e := range entries {
		if e.Directory {
			continue
		}
		h, err := Hash(ctx, e.Source)
		if err != nil || h != e.SHA256 {
			return out, fmt.Errorf("source changed; not removed: %s", e.Source)
		}
	}
	for _, e := range entries {
		if e.Directory {
			continue
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if err := os.Remove(e.Source); err != nil {
			return out, err
		}
		out.Files++
		out.Bytes += e.Size
	}
	dirs := []string{}
	for _, e := range entries {
		if e.Directory {
			dirs = append(dirs, e.Source)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		_ = os.Remove(d)
	}
	return out, nil
}

// listDirectory implements the "list_directory" file-operation: return
// up to MaxFiles entries for a target directory as the Result.Entries
// field. Used by SVSH path tab-completion to enumerate the current cd
// target's filesystem without SMB / WinRM. Pure Go: no shell, no COM.
//
// Spec contract:
//   - spec.Source names the directory to list.
//   - spec.MaxFiles optional cap on entries returned (default 1024, hard
//     ceiling DefaultMaxFiles so a buggy caller cannot DoS the agent).
//   - Non-recursive by design: Tab completion only wants one level.
func listDirectory(spec Spec) (Result, error) {
	var out Result
	src := strings.TrimSpace(spec.Source)
	if src == "" {
		return out, fmt.Errorf("list_directory: source directory required")
	}
	dir := LocalPath(spec.WorkingDir, src)
	info, err := os.Stat(dir)
	if err != nil {
		return out, fmt.Errorf("list_directory: stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return out, fmt.Errorf("list_directory: %s is not a directory", dir)
	}
	cap := spec.MaxFiles
	if cap <= 0 {
		cap = 1024
	}
	if cap > DefaultMaxFiles {
		cap = DefaultMaxFiles
	}
	f, err := os.Open(dir)
	if err != nil {
		return out, fmt.Errorf("list_directory: open %s: %w", dir, err)
	}
	defer f.Close()
	names, err := f.Readdirnames(cap)
	if err != nil && err.Error() != "EOF" {
		return out, fmt.Errorf("list_directory: read %s: %w", dir, err)
	}
	for _, name := range names {
		full := filepath.Join(dir, name)
		sub, serr := os.Lstat(full)
		if serr != nil {
			continue
		}
		entry := Entry{
			Source:    full,
			Name:      name,
			Directory: sub.IsDir(),
			Size:      sub.Size(),
		}
		out.Entries = append(out.Entries, entry)
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		if out.Entries[i].Directory != out.Entries[j].Directory {
			return out.Entries[i].Directory
		}
		return strings.ToLower(out.Entries[i].Name) < strings.ToLower(out.Entries[j].Name)
	})
	out.Files = len(out.Entries)
	return out, nil
}

// writeBytes implements the "write_bytes" file-operation: write (or append)
// a payload to Destination natively — no shell, no PowerShell dependency,
// works regardless of endpoint execution policy.
//
// Phase-1b inline path: spec.Content carries the full payload (JSON base64
// in the signed command). Caller enforces the admin cap (defaults to 100 MB
// via AEGIS_FILE_WRITE_MAX_BYTES) BEFORE calling in; this function trusts
// what it is given and only validates destination path safety.
//
// Future (Phase-1c): spec.ContentID will trigger a FetchContent stream from
// the server for payloads too large to fit in a single signed command.
func writeBytes(spec Spec) (Result, error) {
	var out Result
	if strings.TrimSpace(spec.Destination) == "" {
		return out, fmt.Errorf("write_bytes: destination path required")
	}
	if spec.ContentID != "" && len(spec.Content) > 0 {
		return out, fmt.Errorf("write_bytes: content and content_id are mutually exclusive")
	}
	if spec.ContentID != "" {
		// Streaming via FetchContent is wired at the dispatcher layer (same
		// path pack/unpack use). Basic() runs synchronously without a gRPC
		// client, so the dispatcher calls writeBytes only after streaming
		// the bytes into spec.Content — this guard tells the dispatcher
		// to route via the streaming path first.
		return out, fmt.Errorf("write_bytes: content_id path is dispatched via FetchContent streaming (not handled by Basic)")
	}
	dst := LocalPath(spec.WorkingDir, spec.Destination)
	// Create parent directories best-effort; a missing parent is a
	// legitimate failure for an admin command, so surface the error.
	if parent := filepath.Dir(dst); parent != "" && parent != "." {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return out, fmt.Errorf("write_bytes: create parent %s: %w", parent, err)
		}
	}
	flags := os.O_WRONLY | os.O_CREATE
	if spec.Append {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(dst, flags, 0o644)
	if err != nil {
		return out, fmt.Errorf("write_bytes: open %s: %w", dst, err)
	}
	defer f.Close()
	n, err := f.Write(spec.Content)
	if err != nil {
		return out, fmt.Errorf("write_bytes: write %s: %w", dst, err)
	}
	out.Files = 1
	out.Bytes = int64(n)
	out.StoredPath = dst
	// Content integrity verification: if the signed command names a SHA256
	// of the payload, confirm the bytes on disk match. A mismatch fails
	// the operation; the caller sees the failure in ActionStatus.
	if strings.TrimSpace(spec.SHA256) != "" {
		h := sha256.Sum256(spec.Content)
		got := hex.EncodeToString(h[:])
		if !strings.EqualFold(got, spec.SHA256) {
			return out, fmt.Errorf("write_bytes: SHA256 mismatch: wrote %d bytes, expected=%s got=%s", n, spec.SHA256, got)
		}
		out.SHA256 = got
	}
	return out, nil
}

func Basic(ctx context.Context, spec Spec) (Result, error) {
	var out Result
	if err := ctx.Err(); err != nil {
		return out, err
	}
	// write_bytes uses Destination (not Source) so handle it before the
	// "source required" check below.
	if spec.Operation == "write_bytes" {
		return writeBytes(spec)
	}
	if spec.Operation == "list_directory" {
		return listDirectory(spec)
	}
	if spec.Operation != "remove-manifest" && strings.TrimSpace(spec.Source) == "" {
		return out, fmt.Errorf("source path required")
	}
	path := LocalPath(spec.WorkingDir, spec.Source)
	if spec.Operation == "mkdir" {
		return out, os.MkdirAll(path, 0755)
	}
	if spec.Operation == "touch" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return out, err
		}
		return Result{Files: 1}, f.Close()
	}
	if spec.Operation == "remove-manifest" {
		return DeleteManifest(ctx, spec.Entries)
	}
	if spec.Operation != "delete" {
		return out, fmt.Errorf("unsupported operation %q", spec.Operation)
	}
	matches, err := filepath.Glob(path)
	if err != nil {
		return out, err
	}
	if len(matches) == 0 {
		return out, fmt.Errorf("no files matched %s", path)
	}
	_, maxFiles := spec.Limits()
	var paths []string
	for _, m := range matches {
		info, err := os.Lstat(m)
		if err != nil {
			return out, err
		}
		if info.IsDir() && !spec.Recursive {
			return out, fmt.Errorf("directory requires --recursive: %s", m)
		}
		err = filepath.WalkDir(m, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			paths = append(paths, p)
			if len(paths) > maxFiles {
				return fmt.Errorf("file-count limit exceeded")
			}
			return nil
		})
		if err != nil {
			return out, err
		}
	}
	sort.Slice(paths, func(i, j int) bool { return len(paths[i]) > len(paths[j]) })
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if err := os.Remove(p); err != nil {
			return out, fmt.Errorf("removed %d entries before failure: %w", out.Files, err)
		}
		out.Files++
	}
	return out, nil
}
