package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/aegisoneuem/aegis_one_agent/internal/fileops"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// -----------------------------------------------------------------------------
// UC-22: cross-host cat with > / >> redirection (BigFix-style UNC path syntax)
// -----------------------------------------------------------------------------
//
// Syntax:
//
//	cat <src> > <dst>     overwrite dst with src bytes
//	cat <src> >> <dst>    append src bytes to dst
//
// Either side may be:
//   - a local-to-mockserver path (plain OS path, e.g. C:\tmp\x.txt)
//   - a UNC-style endpoint path  \\<hostname>\<drive>\<path> which the mock
//     translates to <drive>:\<path> on the endpoint named <hostname>.
//
// Flow:
//
//	src remote -> `collect_file` to that endpoint, bytes land in mock temp
//	src local  -> read the mock-local file directly
//	dst remote -> `run_command` on that endpoint with a PowerShell writer that
//	              reconstructs the bytes from a base64-encoded payload
//	              (overwrite OR append)
//	dst local  -> write / append to the mock-local file
//
// nshCatMaxBytes is the mock-side soft cap for a single `cat` operation.
// Phase-1b uses a native `file_operation.write_bytes` dispatch — no
// PowerShell, no cmd-line limit — so this ceiling is now dictated by the
// agent's endpoint cap (AEGIS_FILE_WRITE_MAX_BYTES, default 100 MB) rather
// than Windows CreateProcess bounds. Keep the mock-side value at 100 MB
// so the operator sees a clean error before the dispatch when they ask for
// something larger; the agent is the authoritative enforcer.
const nshCatMaxBytes = 100 * 1024 * 1024

// catEndpoint represents one parsed side of a redirection.
type catEndpoint struct {
	host string // hostname when UNC; "" when local-to-mockserver
	path string // Windows path on the endpoint, or a local OS path
}

// verbCat implements `cat <src> > <dst>` / `cat <src> >> <dst>`.
func (s *nshSession) verbCat(rest string) {
	src, dst, appendMode, err := parseCatRedirect(rest)
	if err != nil {
		s.printf("nsh: %v\n", err)
		s.println(`nsh: usage: cat <src> > <dst>  OR  cat <src> >> <dst>`)
		s.println(`nsh:   <src>/<dst> may be local path or UNC form \\<host>\<drive>\<path>`)
		s.println(`nsh:   with a cd \\host target set, paths without \\<host>\ prefix resolve on that target`)
		return
	}
	// Implicit-target rule: when `cd \\host` set a single target, bare
	// paths (no UNC prefix) mean that target — matches BigFix / BladeLogic
	// nexec intuition "cd puts me on the host, paths are local to it".
	// Relative paths are joined with the current working directory on that
	// target (what `cd <path>` set). Mock-local is used only when no
	// target is set.
	src = s.resolveImplicitCatTarget(src)
	dst = s.resolveImplicitCatTarget(dst)
	bytes, srcDescr, err := s.catReadSource(src)
	if err != nil {
		s.printf("nsh: cat read src failed: %v\n", err)
		return
	}
	if len(bytes) > nshCatMaxBytes {
		s.printf("nsh: cat refuses %d bytes (soft cap %d; endpoint AEGIS_FILE_WRITE_MAX_BYTES is authoritative). Raise the cap or use a smaller transfer.\n", len(bytes), nshCatMaxBytes)
		return
	}
	if err := s.catWriteDest(dst, bytes, appendMode); err != nil {
		s.printf("nsh: cat write dst failed: %v\n", err)
		return
	}
	mode := "overwrite"
	if appendMode {
		mode = "append"
	}
	s.printf("---- cat %s -> %s (%d bytes, %s)\n", srcDescr, catPathDescr(dst), len(bytes), mode)
}

// parseCatRedirect finds the `>` or `>>` token on a whitespace boundary and
// splits the command text into (src, dst, append).
func parseCatRedirect(rest string) (src, dst catEndpoint, appendMode bool, err error) {
	line := strings.TrimSpace(rest)
	if line == "" {
		err = fmt.Errorf("cat needs <src> > <dst>")
		return
	}
	var sep string
	var idx int
	if i := catIndexToken(line, ">>"); i >= 0 {
		sep, idx, appendMode = ">>", i, true
	} else if i := catIndexToken(line, ">"); i >= 0 {
		sep, idx, appendMode = ">", i, false
	} else {
		err = fmt.Errorf("cat is missing the > or >> redirect token")
		return
	}
	left := strings.TrimSpace(line[:idx])
	right := strings.TrimSpace(line[idx+len(sep):])
	if left == "" || right == "" {
		err = fmt.Errorf("cat <src> %s <dst> — one side is empty", sep)
		return
	}
	src = parseCatEndpoint(left)
	dst = parseCatEndpoint(right)
	return
}

// catIndexToken returns the index of tok as a whitespace-bounded token in s.
func catIndexToken(s, tok string) int {
	for i := 0; i+len(tok) <= len(s); i++ {
		if s[i:i+len(tok)] != tok {
			continue
		}
		before := i == 0 || s[i-1] == ' ' || s[i-1] == '\t'
		after := i+len(tok) == len(s) || s[i+len(tok)] == ' ' || s[i+len(tok)] == '\t'
		if before && after {
			return i
		}
	}
	return -1
}

// parseCatEndpoint splits a UNC-style path into {host, local path}.
func parseCatEndpoint(raw string) catEndpoint {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, `\\`) {
		return catEndpoint{path: raw}
	}
	trimmed := strings.TrimPrefix(raw, `\\`)
	parts := strings.SplitN(trimmed, `\`, 3)
	if len(parts) < 3 {
		return catEndpoint{path: raw}
	}
	host := parts[0]
	drive := strings.TrimSuffix(parts[1], ":")
	if drive == "" {
		drive = parts[1]
	}
	rest := parts[2]
	return catEndpoint{host: host, path: strings.ToUpper(drive) + `:\` + rest}
}

// catPathDescr renders an endpoint back as a UNC string for user-facing logs.
func catPathDescr(e catEndpoint) string {
	if e.host == "" {
		return e.path
	}
	drive := e.path
	if len(drive) >= 2 && drive[1] == ':' {
		drive = string(drive[0]) + `\` + drive[3:]
	}
	return `\\` + e.host + `\` + drive
}

// catReadSource fetches the bytes for the src endpoint.
func (s *nshSession) catReadSource(src catEndpoint) ([]byte, string, error) {
	if src.host == "" {
		data, err := os.ReadFile(src.path)
		if err != nil {
			return nil, src.path, err
		}
		return data, src.path, nil
	}
	devID := s.srv.deviceIDByHostname(src.host)
	if devID == "" {
		return nil, catPathDescr(src), fmt.Errorf("unknown host %q (no connected agent with that hostname)", src.host)
	}
	// Use the standard "_nsh-transfer/" prefix so the UploadContent handler
	// stores a fileReceipt we can look up by action_id — avoids guessing
	// the on-disk path.
	up := "_nsh-transfer/" + nshRandomID() + "/cat-src.bin"
	st, cmdID := s.sendTask(devID, "collect_file", &pb.CollectFileSpec{
		Paths:         []string{src.path},
		MaxBytesEach:  int64(nshCatMaxBytes + 1),
		MaxTotalBytes: int64(nshCatMaxBytes + 1),
	}, up)
	if st == nil || st.Phase != pb.ActionPhase_ACTION_PHASE_SUCCESS {
		reason := "collect_file did not complete"
		if st != nil {
			reason = fmt.Sprintf("phase=%s reason=%s", phaseWord(st.Phase), st.Reason)
		}
		return nil, catPathDescr(src), fmt.Errorf("%s", reason)
	}
	receipt, ok := s.takeReceipt(cmdID)
	if !ok {
		return nil, catPathDescr(src), fmt.Errorf("collect_file succeeded but no upload receipt was recorded for cmd=%s", cmdID)
	}
	data, err := os.ReadFile(receipt.path)
	if err != nil {
		return nil, catPathDescr(src), fmt.Errorf("read uploaded blob at %s: %w", receipt.path, err)
	}
	return data, catPathDescr(src), nil
}

// catWriteDest writes bytes to the dst endpoint (overwrite or append).
// Phase 1b: remote writes use the native file_operation write_bytes op —
// no PowerShell, no cmd-line limit, works even when the endpoint has
// PowerShell execution policy locked down.
func (s *nshSession) catWriteDest(dst catEndpoint, data []byte, appendMode bool) error {
	if dst.host == "" {
		if appendMode {
			f, err := os.OpenFile(dst.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = f.Write(data)
			return err
		}
		return os.WriteFile(dst.path, data, 0o644)
	}
	devID := s.srv.deviceIDByHostname(dst.host)
	if devID == "" {
		return fmt.Errorf("unknown host %q (no connected agent with that hostname)", dst.host)
	}
	spec := fileops.Spec{
		Version:        1,
		Operation:      "write_bytes",
		Destination:    dst.path,
		Content:        data,
		Append:         appendMode,
		TimeoutSeconds: 120,
	}
	payload, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal write_bytes spec: %w", err)
	}
	st, _ := s.sendTaskPayload(devID, "file_operation", payload, "")
	if st == nil || st.Phase != pb.ActionPhase_ACTION_PHASE_SUCCESS {
		reason := "file_operation write_bytes did not complete"
		if st != nil {
			reason = fmt.Sprintf("phase=%s exit=%d reason=%s", phaseWord(st.Phase), st.ExitCode, st.Reason)
		}
		return fmt.Errorf("%s", reason)
	}
	return nil
}

// resolveImplicitCatTarget turns a bare-path endpoint into a UNC endpoint
// against the current cd target, so operators do not need to repeat
// \\<host>\<drive>\ for every file after a `cd \\host`. Rules:
//   - endpoint already has host  -> leave as-is (explicit wins)
//   - no cd target               -> leave as-is (mock-local; no implicit host)
//   - multiple cd targets        -> leave as-is (ambiguous; let caller fail)
//   - single cd target + absolute path  -> use target host, keep absolute path
//   - single cd target + relative path  -> use target host + its workingDir
func (s *nshSession) resolveImplicitCatTarget(e catEndpoint) catEndpoint {
	if e.host != "" {
		return e
	}
	if len(s.targets) != 1 {
		return e
	}
	devID := s.targets[0]
	host := s.srv.hostnameOf(devID)
	if host == "" {
		return e
	}
	path := e.path
	if !catIsAbsPath(path) {
		cwd := s.workingDirs[devID]
		if cwd != "" {
			path = catJoinWindowsPath(cwd, path)
		}
	}
	return catEndpoint{host: host, path: path}
}

// catIsAbsPath reports whether p looks like an absolute Windows path
// (drive-letter form "C:\\...") or an rooted path ("\\..." / "/...").
func catIsAbsPath(p string) bool {
	if len(p) >= 2 && p[1] == ':' {
		return true
	}
	if len(p) >= 1 && (p[0] == '\\' || p[0] == '/') {
		return true
	}
	return false
}

// catJoinWindowsPath joins a Windows cwd with a relative name, using
// backslash as the separator the agent-side shells expect.
func catJoinWindowsPath(cwd, rel string) string {
	cwd = strings.TrimRight(cwd, `\/`)
	rel = strings.TrimLeft(rel, `\/`)
	return cwd + `\` + rel
}

// deviceIDByHostname returns the device_id for a given hostname (case-
// insensitive). "" when no live device matches.
func (s *server) deviceIDByHostname(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, rec := range s.byID {
		if strings.EqualFold(rec.Hostname, host) {
			return id
		}
	}
	return ""
}
