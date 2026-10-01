package main

import (
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/patchcache"
	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// installJob is a patch install handed from the receiver to the serial worker.
type installJob struct {
	commandID string
	spec      *pb.InstallPatchSpec
}

// phaseEmitter streams an InstallPhase for the current job (QUEUED is emitted by
// the receiver; VERIFYING/RUNNING by the platform install handler).
type phaseEmitter func(pb.InstallPhase)

// patchInstaller carries everything the serial worker needs across installs.
type patchInstaller struct {
	outbox  chan<- *pb.AgentMessage
	cache   *patchcache.Cache
	tmpDir  string
	timeout time.Duration
}

// patchWorker runs patch installs ONE AT A TIME. The Windows servicing stack
// (CBS/TrustedInstaller) processes a single operation at a time, so concurrent
// DISM installs would conflict — patches must be serialized. It is process-level
// and survives reconnects; it emits phased InstallStatus to the outbox.
//
// Only patch installs go through this serial worker. Other task types (package
// deploy, config, DEX, inventory, commands) are independent of the servicing
// stack and are free to run concurrently.
func (p *patchInstaller) patchWorker(jobs <-chan installJob) {
	for job := range jobs {
		p.runInstall(job)
	}
}

// cacheJanitor periodically enforces the cache TTL and size cap. It runs
// infrequently (config AEGIS_CACHE_JANITOR_INTERVAL) to stay lightweight, and
// logs the outcome so cleanup is visible, never hidden.
func cacheJanitor(cache *patchcache.Cache, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		if err := cache.Enforce(); err != nil {
			slog.Warn("cache janitor: enforcement reported an issue", "error", err.Error())
		} else {
			slog.Debug("cache janitor: enforcement ran")
		}
	}
}

// cacheID is the key a patch is cached under: its KB when present, else the
// expected hash (so a pre-staged patch can be matched at install time).
func cacheID(spec *pb.InstallPatchSpec) string {
	if kb := strings.TrimSpace(spec.Kb); kb != "" {
		return kb
	}
	return strings.TrimSpace(spec.ExpectedHash)
}

func (p *patchInstaller) runInstall(job installJob) {
	cmdID, kb := job.commandID, job.spec.Kb
	emit := func(phase pb.InstallPhase) {
		sendStatus(p.outbox, &pb.InstallStatus{CommandId: cmdID, Kb: kb, Phase: phase})
	}

	// Idempotency guard: if the KB is already installed, skip the whole flow
	// (no decrypt, no DISM) and report success — don't redo applied work.
	if alreadyInstalled(kb) {
		slog.Info("install_patch: already installed, skipping", "command_id", cmdID, "kb", kb)
		sendStatus(p.outbox, &pb.InstallStatus{CommandId: cmdID, Kb: kb,
			Phase: pb.InstallPhase_INSTALL_PHASE_SUCCESS, Detail: "already installed (skipped)"})
		return
	}

	id := cacheID(job.spec)
	installPath := job.spec.FilePath
	fromCache := false

	// Staged path: a pre-cached (encrypted) patch — decrypt it to a temp file.
	// JIT path: install the delivered file directly (Option A; no needless
	// re-encryption of a file we are about to consume).
	if p.cache != nil && id != "" && p.cache.Has(id) {
		slog.Info("install_patch: patch found in cache (staged), using it", "command_id", cmdID, "id", id)
		emit(pb.InstallPhase_INSTALL_PHASE_DOWNLOADING)
		tmp := filepath.Join(p.tmpDir, "install-"+sanitize(cmdID)+p.cache.Ext(id))
		if err := p.cache.Decrypt(id, tmp); err != nil {
			slog.Error("install_patch: cache decrypt failed", "command_id", cmdID, "id", id, "error", err.Error())
			sendStatus(p.outbox, &pb.InstallStatus{CommandId: cmdID, Kb: kb,
				Phase: pb.InstallPhase_INSTALL_PHASE_FAILED, Reason: "cache decrypt failed: " + err.Error()})
			return
		}
		slog.Info("cache: decrypted staged patch to temp", "command_id", cmdID, "id", id, "temp", tmp)
		installPath = tmp
		fromCache = true
	} else {
		slog.Info("install_patch: using delivered file (just-in-time, not cached)", "command_id", cmdID, "file", installPath)
	}

	slog.Info("install_patch: running", "command_id", cmdID, "kb", kb, "from_cache", fromCache)
	st := handleInstallPatch(job.spec, installPath, p.timeout, emit)
	st.CommandId = cmdID

	// Cleanup. The decrypted temp is always removed (it is transient plaintext).
	// A cached patch is deleted after a successful install (patches are
	// single-use per machine); the cache DIRECTORY is always kept.
	if fromCache {
		if err := secure.SecureDelete(installPath); err != nil {
			slog.Warn("cache: could not remove decrypted temp", "temp", installPath, "error", err.Error())
		} else {
			slog.Info("cache: removed decrypted temp after install", "temp", installPath)
		}
		if st.Phase == pb.InstallPhase_INSTALL_PHASE_SUCCESS {
			if err := p.cache.Remove(id); err != nil {
				slog.Warn("cache: could not remove installed patch from cache", "id", id, "error", err.Error())
			} else {
				slog.Info("cache: removed installed patch from cache", "id", id)
			}
		}
	}

	sendStatus(p.outbox, st)
	slog.Info("install_patch: finished",
		"command_id", cmdID, "phase", st.Phase.String(),
		"exit_code", st.ExitCode, "reboot_required", st.RebootRequired, "reason", st.Reason)
}

// sendStatus queues an InstallStatus for the sender goroutine to transmit.
func sendStatus(outbox chan<- *pb.AgentMessage, st *pb.InstallStatus) {
	outbox <- &pb.AgentMessage{Payload: &pb.AgentMessage_InstallStatus{InstallStatus: st}}
}

// sanitize makes a string safe to use in a file name.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, s)
}
