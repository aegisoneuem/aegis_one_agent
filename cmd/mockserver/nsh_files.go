package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/aegisoneuem/aegis_one_agent/internal/fileops"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

type fileReceipt struct{ path, sha256, deviceID string }
type fileContent struct{ path, sha256, deviceID string }
type endpointPath struct {
	device, path string
	directory    bool
}

// Windows backslashes stay literal. Quoting groups paths with spaces without
// applying POSIX shell escaping to drive/UNC paths.
func splitNSHArgs(line string) ([]string, error) {
	var args []string
	var b strings.Builder
	var quote rune
	started := false
	flush := func() {
		if started {
			args = append(args, b.String())
			b.Reset()
			started = false
		}
	}
	for _, r := range line {
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
			continue
		}
		if (r == '"' || r == '\'') && !started {
			quote = r
			started = true
			continue
		}
		if r == ' ' || r == '\t' {
			flush()
			continue
		}
		started = true
		b.WriteRune(r)
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed quote")
	}
	flush()
	return args, nil
}

func (s *nshSession) endpointPaths(raw string) ([]endpointPath, error) {
	var ids []string
	path := raw
	if strings.HasPrefix(raw, `\\`) {
		rest := strings.TrimPrefix(raw, `\\`)
		parts := strings.SplitN(rest, `\`, 2)
		selector := parts[0]
		path = "."
		if len(parts) == 2 {
			path = parts[1]
		}
		if selector == "*" {
			s.srv.liveMu.Lock()
			for id := range s.srv.liveAgents {
				ids = append(ids, id)
			}
			s.srv.liveMu.Unlock()
		} else {
			for _, host := range strings.Split(selector, ",") {
				id, err := s.srv.resolveKnownTarget(host)
				if err != nil {
					return nil, err
				}
				ids = append(ids, id)
			}
		}
		drive := strings.SplitN(path, `\`, 2)
		letter := strings.TrimSuffix(drive[0], "$")
		if len(letter) == 1 && ((letter[0] >= 'a' && letter[0] <= 'z') || (letter[0] >= 'A' && letter[0] <= 'Z')) {
			path = letter + `:\`
			if len(drive) == 2 {
				path += drive[1]
			}
		}
	} else {
		ids = append(ids, s.targets...)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no targets: select a host or use \\\\host\\c\\path")
	}
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("empty path")
	}
	var paths []endpointPath
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			paths = append(paths, endpointPath{device: id, path: path, directory: strings.HasSuffix(raw, `\`) || strings.HasSuffix(raw, "/")})
			seen[id] = true
		}
	}
	return paths, nil
}
func (s *nshSession) fileSpec(op string) fileops.Spec {
	return fileops.Spec{Version: 1, Operation: op, MaxBytes: s.fileMaxBytes, MaxFiles: s.fileMaxFiles, TimeoutSeconds: int(s.timeout)}
}
func (s *nshSession) fileTask(id string, spec fileops.Spec, upload string) (*pb.ActionStatus, string) {
	spec.WorkingDir = s.workingDirs[id]
	payload, err := json.Marshal(spec)
	if err != nil {
		fmt.Println("nsh:", err)
		return nil, ""
	}
	if len(payload) > fileops.MaxManifestBytes {
		fmt.Println("nsh: file manifest exceeds 2 MiB; use smaller batches")
		return nil, ""
	}
	return s.sendTaskPayload(id, "file_operation", payload, upload)
}
func fileSucceeded(st *pb.ActionStatus) bool {
	return st != nil && st.Phase == pb.ActionPhase_ACTION_PHASE_SUCCESS
}
func (s *nshSession) takeReceipt(id string) (fileReceipt, bool) {
	s.srv.pendingMu.Lock()
	defer s.srv.pendingMu.Unlock()
	r, ok := s.srv.fileReceipts[id]
	delete(s.srv.fileReceipts, id)
	return r, ok
}

func (s *nshSession) verbFile(op, rest string) {
	args, err := splitNSHArgs(rest)
	if err != nil {
		fmt.Println("nsh:", err)
		return
	}
	spec := s.fileSpec(op)
	var paths []string
	for _, a := range args {
		switch a {
		case "-r", "--recursive":
			spec.Recursive = true
		case "--overwrite":
			spec.Overwrite = true
		default:
			if strings.HasPrefix(a, "--") {
				fmt.Println("nsh: unknown file option", a)
				return
			}
			paths = append(paths, a)
		}
	}
	need := 1
	if op == "copy" || op == "move" {
		need = 2
	}
	if len(paths) != need {
		fmt.Printf("nsh: %s expects %d path(s); quote paths with spaces\n", op, need)
		return
	}
	sources, err := s.endpointPaths(paths[0])
	if err != nil {
		fmt.Println("nsh:", err)
		return
	}
	if need == 1 {
		s.runFileJobs(sources, func(p endpointPath) bool {
			job := spec
			job.Source = p.path
			st, _ := s.fileTask(p.device, job, "")
			s.printFileResult(p.device, st)
			return fileSucceeded(st)
		})
		return
	}
	destinations, err := s.endpointPaths(paths[1])
	if err != nil {
		fmt.Println("nsh:", err)
		return
	}
	if op == "move" && (len(sources) != 1 || len(destinations) != 1) {
		fmt.Println("nsh: move requires exactly one source host and one destination host; use copy for distribution")
		return
	}
	ok := true
	for _, source := range sources {
		// Local filesystem copy/move avoids a round trip through server storage.
		if len(sources) == 1 && len(destinations) == 1 && source.device == destinations[0].device {
			job := spec
			job.Source = source.path
			job.Destination = destinations[0].path
			job.DestinationDir = destinations[0].directory
			st, _ := s.fileTask(source.device, job, "")
			s.printFileResult(source.device, st)
			ok = fileSucceeded(st) && ok
			continue
		}
		pack := spec
		pack.Operation = "pack"
		pack.Source = source.path
		st, id := s.fileTask(source.device, pack, "_nsh-transfer/"+nshRandomID()+"/bundle.zip")
		receipt, exists := s.takeReceipt(id)
		if !fileSucceeded(st) || !exists || receipt.deviceID != source.device {
			if exists {
				_ = os.Remove(receipt.path)
			}
			s.printFileResult(source.device, st)
			fmt.Println("nsh: source export did not produce a verified transfer artifact")
			ok = false
			continue
		}
		var manifest fileops.Result
		if err = json.Unmarshal([]byte(st.Detail), &manifest); err != nil || manifest.SHA256 != receipt.sha256 {
			os.Remove(receipt.path)
			fmt.Println("nsh: source manifest/archive digest mismatch")
			ok = false
			continue
		}
		copied := s.runFileJobs(destinations, func(dest endpointPath) bool {
			contentID := nshRandomID()
			s.srv.pendingMu.Lock()
			if s.srv.fileContent == nil {
				s.srv.fileContent = make(map[string]fileContent)
			}
			s.srv.fileContent[contentID] = fileContent{path: receipt.path, sha256: receipt.sha256, deviceID: dest.device}
			s.srv.pendingMu.Unlock()
			defer func() { s.srv.pendingMu.Lock(); delete(s.srv.fileContent, contentID); s.srv.pendingMu.Unlock() }()
			job := spec
			job.Operation = "unpack"
			job.Destination = dest.path
			job.DestinationDir = dest.directory
			job.ContentID = contentID
			job.SHA256 = receipt.sha256
			job.Entries = manifest.Entries
			if len(sources) > 1 {
				identity := s.srv.computerIDOf(source.device)
				if identity == "" {
					identity = slugRE.ReplaceAllString(source.device, "_")
				}
				job.Destination = strings.TrimRight(dest.path, `\/`) + `\` + slugify(s.srv.hostnameOf(source.device)) + "-" + identity
				job.DestinationDir = true
			}
			result, _ := s.fileTask(dest.device, job, "")
			s.printFileResult(dest.device, result)
			return fileSucceeded(result)
		})
		if copied && op == "move" {
			remove := spec
			remove.Operation = "remove-manifest"
			remove.Entries = manifest.Entries
			result, _ := s.fileTask(source.device, remove, "")
			s.printFileResult(source.device, result)
			copied = fileSucceeded(result)
		}
		_ = os.Remove(receipt.path)
		_ = os.Remove(filepath.Dir(receipt.path))
		ok = copied && ok
	}
	s.recordFileExit(ok)
}
func (s *nshSession) printFileResult(id string, st *pb.ActionStatus) {
	if st == nil {
		fmt.Printf("---- %s FILE FAILED (not sent/no terminal reply)\n", s.srv.hostnameOf(id))
		return
	}
	fmt.Printf("---- %s %s: %s\n", s.srv.hostnameOf(id), phaseWord(st.Phase), st.Reason)
}
func (s *nshSession) recordFileExit(ok bool) {
	if s.engine != nil {
		exit := 0
		reason := "file job completed"
		if !ok {
			exit = 1
			reason = "one or more file operations failed"
		}
		s.engine.recordLastResult("", "", "", reason, exit, 0)
	}
}
func (s *nshSession) runFileJobs(paths []endpointPath, job func(endpointPath) bool) bool {
	workers := s.fileWorkers
	if workers <= 0 {
		workers = 4
	}
	if workers > len(paths) {
		workers = len(paths)
	}
	jobs := make(chan endpointPath)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := true
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				if !job(p) {
					mu.Lock()
					ok = false
					mu.Unlock()
				}
			}
		}()
	}
	for _, p := range paths {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
	s.recordFileExit(ok)
	return ok
}
func (s *nshSession) setFileOption(key, value string) bool {
	switch key {
	case "file-workers":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 32 {
			fmt.Println("nsh: file-workers must be 1..32")
			return true
		}
		s.fileWorkers = n
	case "file-max-bytes":
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 1 {
			fmt.Println("nsh: file-max-bytes must be positive")
			return true
		}
		s.fileMaxBytes = n
	case "file-max-files":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			fmt.Println("nsh: file-max-files must be positive")
			return true
		}
		s.fileMaxFiles = n
	default:
		return false
	}
	fmt.Printf("%s = %s\n", key, value)
	return true
}
