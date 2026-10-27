package softdist

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const BundleManifestName = "aegis-package.json"
const DefaultBundleMaxBytes int64 = 4 << 30
const DefaultBundleMaxFiles = 10000
const bundleManifestLimit = 4 << 20

type BundleLimits struct {
	MaxBytes int64
	MaxFiles int
}

func (l BundleLimits) defaults() BundleLimits {
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultBundleMaxBytes
	}
	if l.MaxBytes == 1<<63-1 {
		l.MaxBytes--
	}
	if l.MaxFiles <= 0 {
		l.MaxFiles = DefaultBundleMaxFiles
	}
	return l
}

type BundleFile struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256,omitempty"`
	Directory bool   `json:"directory,omitempty"`
}
type BundleManifest struct {
	Version int          `json:"version"`
	Files   []BundleFile `json:"files"`
}
type BundleOptions struct {
	EntryPoint, Command, Destination, Cleanup, InstallerType, ManagedRoot string
	Session                                                               *Session
	Limits                                                                BundleLimits
}

func (o BundleOptions) Validate() error {
	if strings.EqualFold(strings.TrimSpace(o.InstallerType), "command") {
		if strings.TrimSpace(o.Command) == "" || o.EntryPoint != "" {
			return fmt.Errorf("command bundle needs bundle.command and no entry_point")
		}
	} else {
		if o.Command != "" || o.EntryPoint == "" {
			return fmt.Errorf("installer bundle needs entry_point and no bundle.command")
		}
		if _, err := bundleName(o.EntryPoint); err != nil {
			return fmt.Errorf("invalid bundle entry_point: %w", err)
		}
	}
	if o.Destination != "" {
		if !filepath.IsAbs(o.Destination) || filepath.Clean(o.Destination) != o.Destination || filepath.Dir(o.Destination) == o.Destination {
			return fmt.Errorf("bundle destination must be an absolute, clean, new directory path")
		}
		if err := bundleLocalDestination(o.Destination); err != nil {
			return err
		}
	}
	if o.Cleanup != "" && o.Cleanup != "always" && o.Cleanup != "retain" {
		return fmt.Errorf("bundle cleanup must be always or retain")
	}
	if o.Cleanup == "retain" && o.Destination == "" {
		return fmt.Errorf("retaining bundle files requires an explicit custom destination")
	}
	return nil
}
func bundleName(name string) (string, error) {
	if !utf8.ValidString(name) || name == "" || strings.Contains(name, "\\") || strings.EqualFold(name, BundleManifestName) {
		return "", fmt.Errorf("invalid/reserved package path")
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.TrimRight(part, " .") != part || strings.ContainsAny(part, "<>:\"|?*\x00") {
			return "", fmt.Errorf("unsafe package path")
		}
		for _, r := range part {
			if r < 32 {
				return "", fmt.Errorf("control character in package path")
			}
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		base = strings.NewReplacer("¹", "1", "²", "2", "³", "3").Replace(base)
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CLOCK$" || base == "CONIN$" || base == "CONOUT$" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
			return "", fmt.Errorf("reserved Windows package path")
		}
	}
	return filepath.FromSlash(name), nil
}

type bundleReader struct {
	ctx context.Context
	r   io.Reader
}

func (r bundleReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
func bundleHash(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, bundleReader{ctx, f}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// PackBundle is native package authoring: files + directories + SHA256 manifest.
// It refuses links, output inside input, and an existing output file.
func PackBundle(ctx context.Context, source, output string, limits BundleLimits) (string, error) {
	limits = limits.defaults()
	source, err := filepath.Abs(source)
	if err != nil {
		return "", err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return "", err
	}
	if rel, e := filepath.Rel(source, output); e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("bundle output must be outside the source folder")
	}
	if err = bundleNoLinks(source); err != nil {
		return "", err
	}
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("bundle source must be an existing directory")
	}
	m := BundleManifest{Version: 1}
	var total int64
	err = filepath.WalkDir(source, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == source {
			return nil
		}
		if err := bundleNoLinks(path); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("nonregular package member refused")
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if _, err = bundleName(name); err != nil {
			return err
		}
		e := BundleFile{Path: name, Directory: info.IsDir()}
		if !e.Directory {
			e.Size = info.Size()
			if e.Size > limits.MaxBytes-total {
				return fmt.Errorf("package exceeds expanded byte limit")
			}
			total += e.Size
			e.SHA256, err = bundleHash(ctx, path)
			if err != nil {
				return err
			}
		}
		m.Files = append(m.Files, e)
		if len(m.Files) > limits.MaxFiles {
			return fmt.Errorf("package exceeds file limit")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(m.Files) == 0 {
		return "", fmt.Errorf("package source is empty")
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	if err = validateBundleManifest(m, limits); err != nil {
		return "", err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	if len(raw) > bundleManifestLimit {
		return "", fmt.Errorf("package manifest exceeds size limit")
	}
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	success := false
	defer func() {
		f.Close()
		if !success {
			_ = os.Remove(output)
		}
	}()
	z := zip.NewWriter(f)
	defer z.Close()
	w, err := z.Create(BundleManifestName)
	if err != nil {
		return "", err
	}
	if _, err = w.Write(raw); err != nil {
		return "", err
	}
	for _, e := range m.Files {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		name := e.Path
		mode := os.FileMode(0600)
		if e.Directory {
			name += "/"
			mode = os.ModeDir | 0700
		}
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(mode)
		w, err = z.CreateHeader(h)
		if err != nil {
			return "", err
		}
		if e.Directory {
			continue
		}
		path := filepath.Join(source, filepath.FromSlash(e.Path))
		if err = bundleNoLinks(path); err != nil {
			return "", err
		}
		in, err := os.Open(path)
		if err != nil {
			return "", err
		}
		hash := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(w, hash), io.LimitReader(bundleReader{ctx, in}, e.Size+1))
		in.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if n != e.Size || hex.EncodeToString(hash.Sum(nil)) != e.SHA256 {
			return "", fmt.Errorf("package source changed while packing")
		}
	}
	if err = z.Close(); err != nil {
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	sum, err := bundleHash(ctx, output)
	if err != nil {
		return "", err
	}
	success = true
	return sum, nil
}
func validateBundleManifest(m BundleManifest, limits BundleLimits) error {
	if m.Version != 1 || len(m.Files) == 0 || len(m.Files) > limits.MaxFiles {
		return fmt.Errorf("invalid package manifest version/count")
	}
	seen := map[string]bool{}
	var total int64
	for _, e := range m.Files {
		if _, err := bundleName(e.Path); err != nil {
			return err
		}
		key := strings.ToLower(e.Path)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate package path")
		}
		seen[key] = e.Directory
		if e.Directory {
			if e.Size != 0 || e.SHA256 != "" {
				return fmt.Errorf("invalid directory manifest entry")
			}
			continue
		}
		if e.Size < 0 || e.Size > limits.MaxBytes-total {
			return fmt.Errorf("package exceeds expanded byte limit")
		}
		total += e.Size
		if hash, err := hex.DecodeString(e.SHA256); err != nil || len(hash) != sha256.Size {
			return fmt.Errorf("invalid package member SHA256")
		}
	}
	for path := range seen {
		for parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(path))); parent != "."; parent = filepath.ToSlash(filepath.Dir(filepath.FromSlash(parent))) {
			if dir, exists := seen[parent]; exists && !dir {
				return fmt.Errorf("package file/directory path conflict")
			}
		}
	}
	return nil
}

// PreparedBundle owns an opened directory handle through execution/cleanup.
// Rooted removal never traverses links out of that owned tree.
type PreparedBundle struct {
	Directory, EntryPath, EntryHash string
	Retain                          bool
	root                            *os.Root
	identity                        os.FileInfo
	finalized                       bool
	state                           string
	finalErr                        error
}

func (b *PreparedBundle) Finalize() (string, error) {
	if b.finalized {
		return b.state, b.finalErr
	}
	b.finalized = true
	if b.Retain {
		b.state = "retained"
		b.finalErr = b.root.Close()
		if b.finalErr != nil {
			b.state = "failed"
		}
		return b.state, b.finalErr
	}
	b.state = "removed"
	b.finalErr = b.remove()
	if b.finalErr != nil {
		b.state = "failed"
	}
	return b.state, b.finalErr
}
func (b *PreparedBundle) reopenOwned(path string) error {
	b.root = nil
	b.Directory = path
	if err := bundleNoLinks(path); err != nil {
		return err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	identity, err := root.Stat(".")
	if err != nil {
		root.Close()
		return err
	}
	if !os.SameFile(b.identity, identity) {
		root.Close()
		return fmt.Errorf("package directory identity changed; refusing unknown tree")
	}
	b.root = root
	return nil
}
func (b *PreparedBundle) remove() error {
	if b.root == nil {
		return fmt.Errorf("owned package directory handle unavailable; cleanup refused")
	}
	f, err := b.root.Open(".")
	if err != nil {
		b.root.Close()
		return err
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		b.root.Close()
		return err
	}
	var failures []error
	for _, e := range entries {
		if err = b.root.RemoveAll(e.Name()); err != nil {
			failures = append(failures, err)
		}
	}
	if err = b.root.Close(); err != nil {
		failures = append(failures, err)
	}
	current, err := os.Lstat(b.Directory)
	if err != nil {
		failures = append(failures, err)
	} else if !os.SameFile(b.identity, current) {
		failures = append(failures, fmt.Errorf("package directory identity changed; path removal refused"))
	} else if err = os.Remove(b.Directory); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}
func PrepareBundle(ctx context.Context, archive, expectedHash string, o BundleOptions) (prepared *PreparedBundle, err error) {
	if err = o.Validate(); err != nil {
		return nil, err
	}
	limits := o.Limits.defaults()
	if hash, e := hex.DecodeString(expectedHash); e != nil || len(hash) != sha256.Size {
		return nil, fmt.Errorf("bundle requires a pinned SHA256")
	}
	archiveFile, err := openBundleArchive(archive)
	if err != nil {
		return nil, err
	}
	defer archiveFile.Close()
	info, err := archiveFile.Stat()
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	if _, err = io.Copy(h, bundleReader{ctx, archiveFile}); err != nil {
		return nil, err
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), expectedHash) {
		return nil, fmt.Errorf("bundle integrity check failed (SHA256 mismatch)")
	}
	if err = inspectBundleDirectory(archiveFile, info.Size(), limits.MaxFiles); err != nil {
		return nil, err
	}
	z, err := zip.NewReader(archiveFile, info.Size())
	if err != nil {
		return nil, err
	}
	if len(z.File) > limits.MaxFiles+1 {
		return nil, fmt.Errorf("bundle exceeds member limit")
	}
	var manifestZip *zip.File
	for _, f := range z.File {
		if strings.EqualFold(f.Name, BundleManifestName) {
			if manifestZip != nil || f.Name != BundleManifestName {
				return nil, fmt.Errorf("duplicate/aliased bundle manifest")
			}
			manifestZip = f
		}
	}
	if manifestZip == nil || manifestZip.UncompressedSize64 > bundleManifestLimit || !manifestZip.Mode().IsRegular() {
		return nil, fmt.Errorf("bundle manifest missing or invalid")
	}
	r, err := manifestZip.Open()
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(bundleReader{ctx, r}, bundleManifestLimit+1))
	r.Close()
	if err != nil {
		return nil, err
	}
	if len(raw) > bundleManifestLimit {
		return nil, fmt.Errorf("bundle manifest too large")
	}
	var manifest BundleManifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("invalid package manifest JSON")
	}
	if err = validateBundleManifest(manifest, limits); err != nil {
		return nil, err
	}
	if len(z.File) != len(manifest.Files)+1 {
		return nil, fmt.Errorf("archive/manifest member count differs")
	}
	declared := map[string]BundleFile{}
	for _, f := range manifest.Files {
		declared[f.Path] = f
	}
	if o.EntryPoint != "" {
		e, ok := declared[o.EntryPoint]
		if !ok || e.Directory {
			return nil, fmt.Errorf("bundle entry_point is not a manifested file")
		}
	}
	parent := o.ManagedRoot
	if o.Destination != "" {
		parent = filepath.Dir(o.Destination)
		if _, e := os.Lstat(o.Destination); !os.IsNotExist(e) {
			return nil, fmt.Errorf("custom bundle destination already exists or cannot be inspected")
		}
	}
	if parent == "" {
		return nil, fmt.Errorf("bundle staging root is required")
	}
	if err = bundleNoLinks(parent); err != nil {
		return nil, err
	}
	if o.Destination == "" {
		if err = os.MkdirAll(parent, 0700); err != nil {
			return nil, err
		}
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	stage := filepath.Join(parent, "aegis-package-"+hex.EncodeToString(nonce[:]))
	if err = makeBundleDirectory(stage, o.Session); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(stage)
	if err != nil {
		os.Remove(stage)
		return nil, err
	}
	identity, err := root.Stat(".")
	if err != nil {
		root.Close()
		os.Remove(stage)
		return nil, err
	}
	b := &PreparedBundle{Directory: stage, root: root, identity: identity}
	defer func() {
		if err != nil {
			b.Retain = false
			_, cleanupErr := b.Finalize()
			err = errors.Join(err, cleanupErr)
		}
	}()
	seen := map[string]bool{}
	for _, zf := range z.File {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if zf == manifestZip {
			continue
		}
		name := strings.TrimSuffix(zf.Name, "/")
		rel, nameErr := bundleName(name)
		if nameErr != nil {
			return nil, nameErr
		}
		e, exists := declared[name]
		if !exists || seen[name] || zf.FileInfo().IsDir() != e.Directory {
			return nil, fmt.Errorf("archive/manifest member mismatch")
		}
		seen[name] = true
		if zf.Mode()&os.ModeSymlink != 0 || (!e.Directory && !zf.Mode().IsRegular()) {
			return nil, fmt.Errorf("bundle link/nonregular member refused")
		}
		if e.Directory {
			if err = root.MkdirAll(rel, 0700); err != nil {
				return nil, err
			}
			continue
		}
		if zf.UncompressedSize64 != uint64(e.Size) {
			return nil, fmt.Errorf("package member size mismatch")
		}
		if err = root.MkdirAll(filepath.Dir(rel), 0700); err != nil {
			return nil, err
		}
		out, openErr := root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if openErr != nil {
			return nil, openErr
		}
		in, openErr := zf.Open()
		if openErr != nil {
			out.Close()
			return nil, openErr
		}
		h := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(out, h), io.LimitReader(bundleReader{ctx, in}, e.Size+1))
		in.Close()
		closeErr := out.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if n != e.Size || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), e.SHA256) {
			return nil, fmt.Errorf("package member integrity check failed")
		}
	}
	if o.Destination != "" {
		if err = bundleNoLinks(parent); err != nil {
			return nil, err
		}
		// Windows directory handles opened by os.Root prevent renaming. Close,
		// publish without replacement, then reopen and verify the same file ID.
		if err = b.root.Close(); err != nil {
			b.root = nil
			return nil, err
		}
		if publishErr := publishBundle(stage, o.Destination); publishErr != nil {
			reopenErr := b.reopenOwned(stage)
			return nil, errors.Join(publishErr, reopenErr)
		}
		if err = b.reopenOwned(o.Destination); err != nil {
			return nil, err
		}
	}
	b.Retain = o.Cleanup == "retain" || (o.Cleanup == "" && o.Destination != "")
	if o.EntryPoint != "" {
		b.EntryPath = filepath.Join(b.Directory, filepath.FromSlash(o.EntryPoint))
		b.EntryHash = declared[o.EntryPoint].SHA256
	}
	return b, nil
}
