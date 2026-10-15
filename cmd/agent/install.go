package main

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/patch"
	"github.com/aegisoneuem/aegis_one_agent/internal/patchcache"
	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// installJob is a patch install (or uninstall) handed from the receiver to the
// serial worker. Both go through the same worker because the Windows servicing
// stack processes one operation at a time.
type installJob struct {
	commandID string
	spec      *pb.InstallPatchSpec
	uninstall bool // true = remove the KB (wusa) instead of install (DISM)
}

// servicingBusy is true while the agent is running its OWN install/uninstall
// (DISM/wusa). The CBS servicing watch uses it to DEFER the post-change re-scan
// until the agent's op finishes — the agent's own CBS churn is expected and is
// already reported via ActionStatus, so scanning mid-install is wasteful. External
// (manual) changes happen while this is false and trigger a scan as normal.
var servicingBusy atomic.Bool

// servicingGate serializes the agent's OWN install/uninstall against the heavy
// offline compliance scan. Both pound the same Windows Update / TrustedInstaller
// plumbing, so running them together starves each other — a wusa install that
// takes ~3 min alone can drag to 15+ min while a scan runs, and the scan reads a
// half-applied state (wrong "installed"/"missing" counts). The install/uninstall
// takes the lock (waiting out an in-flight scan, bounded); the scan TryLock-skips
// and is re-triggered after the op settles (via the CBS watch). Only one heavy
// servicing op touches WU at a time.
var servicingGate sync.Mutex

// machineInstallGate serializes EVERY machine-mutating install on this endpoint —
// patch install/uninstall AND software deploy — down to one at a time. This is an
// OS constraint, not ours: Windows Installer allows only one MSI transaction at a
// time (the global _MSIExecute mutex → exit 1618), and a CBS/TrustedInstaller
// servicing op plus a concurrent MSI collide via pending-reboot state and file
// locks. Patches and software deploys run on SEPARATE queues (so neither blocks
// the other's intake), but both acquire this gate around the actual OS execution,
// so only one installer ever touches the machine at once. Lock order is always
// machineInstallGate → servicingGate (softdist never takes servicingGate; the
// compliance scan never takes this gate) → no deadlock.
var machineInstallGate sync.Mutex

// phaseEmitter streams an ActionPhase for the current job (QUEUED is emitted by
// the receiver; VERIFYING/RUNNING by the platform install handler).
type phaseEmitter func(pb.ActionPhase)

// patchInstaller carries everything the serial worker needs across installs.
type patchInstaller struct {
	outbox       chan<- *pb.AgentMessage
	cache        *patchcache.Cache
	tmpDir       string
	deviceID     string
	rebootStore  *rebootPendingStore
	installedSet *installedStore
	timeout      time.Duration
	maxAttempts  int
	retryBackoff time.Duration
}

// isInstalled reports whether the KB is already present, using the AUTHORITATIVE
// last-scan installed set first (WUA ground truth) and falling back to the cheap
// CBS check. The CBS check alone misses some updates, so the scan set is what
// lets the agent reliably skip a redundant install of an already-applied patch.
func (p *patchInstaller) isInstalled(kb string) bool {
	if p.installedSet != nil && p.installedSet.has(kb) {
		return true
	}
	return alreadyInstalled(kb)
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
		job := job
		// Isolate each job: a panic in one must not kill the worker (which would
		// stop ALL future installs) — recover and move to the next job.
		if job.uninstall {
			safeRun("patch-uninstall", func() { p.runUninstall(job) })
		} else {
			safeRun("patch-install", func() { p.runInstall(job) })
		}
	}
}

// cacheJanitor periodically enforces the cache TTL and size cap. It runs
// infrequently (config AEGIS_CACHE_JANITOR_INTERVAL) to stay lightweight, and
// logs the outcome so cleanup is visible, never hidden.
func cacheJanitor(cache *patchcache.Cache, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		safeRun("cache-janitor", func() {
			if err := cache.Enforce(); err != nil {
				slog.Warn("cache janitor: enforcement reported an issue", "error", err.Error())
			} else {
				slog.Debug("cache janitor: enforcement ran")
			}
		})
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
	// One installer at a time across the whole machine (patch + softdist). Acquired
	// first so it is released last (see machineInstallGate).
	machineInstallGate.Lock()
	defer machineInstallGate.Unlock()
	servicingBusy.Store(true)
	// Serialize against the compliance scan: wait out any in-flight scan, and keep
	// scans from starting while we install (they contend on WU/TrustedInstaller).
	if !servicingGate.TryLock() {
		slog.Info("install_patch: waiting for an in-flight compliance scan to finish before installing",
			"command_id", job.commandID, "kb", job.spec.Kb)
		servicingGate.Lock()
	}
	defer func() {
		servicingGate.Unlock()
		servicingBusy.Store(false)
	}()
	cmdID, kb := job.commandID, job.spec.Kb
	started := time.Now().Unix()
	emit := func(phase pb.ActionPhase) {
		sendStatus(p.outbox, &pb.ActionStatus{
			ActionId: cmdID, ActionType: actionPatchInstall, DeviceId: p.deviceID,
			Kb: kb, Phase: phase, StartedAt: started,
		})
	}

	// Idempotency guard: if the KB is already installed, skip the whole flow
	// (no decrypt, no DISM) and report NOT_APPLICABLE — don't redo applied work.
	if p.isInstalled(kb) {
		slog.Info("install_patch: already installed, skipping", "command_id", cmdID, "kb", kb)
		sendStatus(p.outbox, &pb.ActionStatus{
			ActionId: cmdID, ActionType: actionPatchInstall, DeviceId: p.deviceID, Kb: kb,
			Phase: pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE, Reason: "already installed (skipped)",
			StartedAt: started, FinishedAt: time.Now().Unix(),
		})
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
		emit(pb.ActionPhase_ACTION_PHASE_DOWNLOADING)
		tmp := filepath.Join(p.tmpDir, "install-"+sanitize(cmdID)+p.cache.Ext(id))
		if err := p.cache.Decrypt(id, tmp); err != nil {
			slog.Error("install_patch: cache decrypt failed", "command_id", cmdID, "id", id, "error", err.Error())
			sendStatus(p.outbox, &pb.ActionStatus{
				ActionId: cmdID, ActionType: actionPatchInstall, DeviceId: p.deviceID, Kb: kb,
				Phase: pb.ActionPhase_ACTION_PHASE_FAILED, Reason: "cache decrypt failed: " + err.Error(),
				StartedAt: started, FinishedAt: time.Now().Unix(),
			})
			return
		}
		slog.Info("cache: decrypted staged patch to temp", "command_id", cmdID, "id", id, "temp", tmp)
		installPath = tmp
		fromCache = true
	} else {
		slog.Info("install_patch: using delivered file (just-in-time, not cached)", "command_id", cmdID, "file", installPath)
	}

	slog.Info("install_patch: running", "command_id", cmdID, "kb", kb, "from_cache", fromCache)
	stopPings := p.startProgressPings(cmdID, actionPatchInstall, kb, started)
	st := handleInstallPatch(job.spec, installPath, p.timeout, p.maxAttempts, p.retryBackoff, emit)
	stopPings()
	// Stamp the action-correlation fields the console tracks by (handleInstallPatch
	// only fills the install-specific fields).
	st.ActionId = cmdID
	st.ActionType = actionPatchInstall
	st.DeviceId = p.deviceID
	st.StartedAt = started
	st.FinishedAt = time.Now().Unix()

	// Cleanup. The decrypted temp is always removed (it is transient plaintext).
	// A cached patch is deleted after a successful install (patches are
	// single-use per machine); the cache DIRECTORY is always kept.
	if fromCache {
		if err := secure.SecureDelete(installPath); err != nil {
			slog.Warn("cache: could not remove decrypted temp", "temp", installPath, "error", err.Error())
		} else {
			slog.Info("cache: removed decrypted temp after install", "temp", installPath)
		}
		if st.Phase == pb.ActionPhase_ACTION_PHASE_SUCCESS {
			if err := p.cache.Remove(id); err != nil {
				slog.Warn("cache: could not remove installed patch from cache", "id", id, "error", err.Error())
			} else {
				slog.Info("cache: removed installed patch from cache", "id", id)
			}
		}
	}

	// Track KBs that need a reboot so the device's pending-reboot report can name
	// which patches are waiting (cleared once the OS pending-reboot flag is gone).
	if st.Phase == pb.ActionPhase_ACTION_PHASE_SUCCESS && st.RebootRequired && p.rebootStore != nil {
		p.rebootStore.add(kb)
	}

	sendStatus(p.outbox, st)
	slog.Info("install_patch: finished",
		"command_id", cmdID, "phase", st.Phase.String(),
		"exit_code", st.ExitCode, "reboot_required", st.RebootRequired, "reason", st.Reason)
}

// runUninstall removes an installed KB via wusa (idempotent: if the KB is not
// installed, it reports NOT_APPLICABLE without running wusa). Runs on the same
// serial worker as installs (the servicing stack is single-operation).
func (p *patchInstaller) runUninstall(job installJob) {
	// One installer at a time across the whole machine (patch + softdist).
	machineInstallGate.Lock()
	defer machineInstallGate.Unlock()
	servicingBusy.Store(true)
	servicingGate.Lock()
	defer func() {
		servicingGate.Unlock()
		servicingBusy.Store(false)
	}()
	cmdID, kb := job.commandID, job.spec.Kb
	started := time.Now().Unix()

	if !p.isInstalled(kb) {
		slog.Info("uninstall_patch: not installed, nothing to do", "command_id", cmdID, "kb", kb)
		sendStatus(p.outbox, &pb.ActionStatus{
			ActionId: cmdID, ActionType: actionPatchUninstall, DeviceId: p.deviceID, Kb: kb,
			Phase: pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE, Reason: "not installed (nothing to uninstall)",
			StartedAt: started, FinishedAt: time.Now().Unix(),
		})
		return
	}

	sendStatus(p.outbox, &pb.ActionStatus{
		ActionId: cmdID, ActionType: actionPatchUninstall, DeviceId: p.deviceID, Kb: kb,
		Phase: pb.ActionPhase_ACTION_PHASE_RUNNING, StartedAt: started,
	})
	slog.Info("uninstall_patch: running", "command_id", cmdID, "kb", kb)
	stopPings := p.startProgressPings(cmdID, actionPatchUninstall, kb, started)
	res := patch.UninstallKB(kb, p.timeout)
	stopPings()

	st := &pb.ActionStatus{
		ActionId: cmdID, ActionType: actionPatchUninstall, DeviceId: p.deviceID, Kb: kb,
		ExitCode: int32(res.ExitCode), RebootRequired: res.RebootRequired, Detail: res.Detail,
		StartedAt: started, FinishedAt: time.Now().Unix(),
	}
	if res.Success {
		st.Phase = pb.ActionPhase_ACTION_PHASE_SUCCESS
		if res.RebootRequired {
			st.Reason = "uninstalled — RESTART REQUIRED to complete"
		} else {
			st.Reason = "uninstalled — completed, no restart needed"
		}
		// Drop it from the authoritative installed set NOW so a quick reinstall is not
		// wrongly skipped before the post-uninstall rescan refreshes the set.
		p.installedSet.remove(kb)
		// A reboot may be needed to finalize the removal — track it so the device's
		// pending-reboot report names this KB.
		if res.RebootRequired && p.rebootStore != nil {
			p.rebootStore.add(kb)
		}
	} else {
		st.Phase = pb.ActionPhase_ACTION_PHASE_FAILED
		st.Reason = res.Reason
	}
	sendStatus(p.outbox, st)
	slog.Info("uninstall_patch: finished", "command_id", cmdID, "kb", kb,
		"phase", st.Phase.String(), "exit_code", st.ExitCode, "reboot_required", st.RebootRequired, "reason", st.Reason)
}

// progressPingInterval is how often the agent re-sends a RUNNING ActionStatus to
// the server during a long install/uninstall, so the console shows the action is
// still progressing (not stuck) — like BigFix's running state.
const progressPingInterval = 30 * time.Second

// startProgressPings streams periodic RUNNING updates (with elapsed time) to the
// server for a long-running action, and returns a stop func to call when it ends.
func (p *patchInstaller) startProgressPings(cmdID, actionType, kb string, started int64) func() {
	stop := make(chan struct{})
	go func() {
		defer recoverLog("progress-pings")
		t := time.NewTicker(progressPingInterval)
		defer t.Stop()
		begin := time.Now()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				sendStatus(p.outbox, &pb.ActionStatus{
					ActionId: cmdID, ActionType: actionType, DeviceId: p.deviceID, Kb: kb,
					Phase:     pb.ActionPhase_ACTION_PHASE_RUNNING,
					Reason:    fmt.Sprintf("in progress (elapsed %s)", time.Since(begin).Round(time.Second)),
					StartedAt: started,
				})
			}
		}
	}()
	return func() { close(stop) }
}

// sendStatus queues an ActionStatus for the sender goroutine to transmit.
func sendStatus(outbox chan<- *pb.AgentMessage, st *pb.ActionStatus) {
	outbox <- actionStatusMsg(st)
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
