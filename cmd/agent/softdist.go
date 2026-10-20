package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/patchcache"
	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// softdistJob is a software deploy handed to the serial softdist worker. Software
// deploys run on their OWN queue (independent of patches so neither blocks the
// other's intake), but both acquire machineInstallGate around the actual OS
// install, so only one installer ever runs at a time (see install.go).
//
// cacheID non-empty → install the ENCRYPTED, server-delivered installer from the
// cache (decrypt to a temp, run, secure-delete). Empty → install spec.FilePath
// directly (a pre-staged / admin-dropped local file; the dev/local option).
type softdistJob struct {
	commandID string
	spec      *pb.SoftwareDeploySpec
	cacheID   string // non-empty → install from the encrypted cache
	localPath string // resolved local installer path (when cacheID is empty)
}

// softdistInstaller carries what the worker + delivery need across deploys.
type softdistInstaller struct {
	outbox      chan<- *pb.AgentMessage
	deviceID    string
	cache       *patchcache.Cache // encrypted-at-rest store, shared with patches
	tmpDir      string            // transient decrypt-to-install dir (secure-deleted after)
	downloadDir string            // controlled drop zone for pre-staged local installers
	jobs        chan softdistJob
}

func newSoftdistInstaller(outbox chan<- *pb.AgentMessage, deviceID string, cache *patchcache.Cache, tmpDir, downloadDir string) *softdistInstaller {
	return &softdistInstaller{outbox: outbox, deviceID: deviceID, cache: cache, tmpDir: tmpDir, downloadDir: downloadDir, jobs: make(chan softdistJob, 32)}
}

// resolveLocal resolves a pre-staged installer path. A bare name / relative path
// is taken under SKVC\download (the controlled drop zone — the documented place
// for admin/console-dropped installers); an absolute path is honored as-is (dev /
// explicit). Server-delivered packages never use this — they stream straight into
// the encrypted cache.
func (s *softdistInstaller) resolveLocal(fp string) string {
	if filepath.IsAbs(fp) {
		return fp
	}
	return filepath.Join(s.downloadDir, fp)
}

// deliver is the dispatch entry (runs on the receiver goroutine) — it must NOT
// block. It decides how the installer reaches the machine, then enqueues the
// install job:
//   - spec.file_path present AND on disk → pre-staged/local file → enqueue now.
//   - content_id already cached → enqueue now (install from the encrypted cache).
//   - content_id not cached → STREAM it from the server in the background (peer-
//     cache / FetchContent), store it encrypted, then enqueue — the real-UEM path.
func (s *softdistInstaller) deliver(ctx context.Context, client pb.AgentControlPlaneClient, cmdID string, spec *pb.SoftwareDeploySpec) {
	// 0. Idempotency / detection (Level 1, MSI): if a product_code is given, skip the
	//    no-op — install of an already-present product, or uninstall of an absent one.
	//    This avoids even downloading an installer we would not run.
	action := strings.ToLower(strings.TrimSpace(spec.Action))
	if action == "" {
		action = "install"
	}
	if pc := strings.TrimSpace(spec.ProductCode); pc != "" {
		installed := softdist.ProductInstalled(pc)
		switch {
		case action == "install" && installed:
			slog.Info("software_deploy: product already installed, skipping", "command_id", cmdID, "product_code", pc)
			send(ctx, s.outbox, actionStatusMsg(simpleActionStatus(cmdID, actionSoftwareDeploy, s.deviceID, "",
				pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE, "already installed (skipped)")))
			return
		case action == "uninstall" && !installed:
			slog.Info("software_deploy: product not installed, nothing to uninstall", "command_id", cmdID, "product_code", pc)
			send(ctx, s.outbox, actionStatusMsg(simpleActionStatus(cmdID, actionSoftwareDeploy, s.deviceID, "",
				pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE, "not installed (nothing to uninstall)")))
			return
		}
	}

	// 0a. Idempotency / detection for apps in the registry Uninstall hive, matched by
	//     uninstall_key / display_name — VERSION-AWARE when spec.version is set:
	//     absent → install; OLDER version installed → proceed (upgrade); same/newer →
	//     skip. Without a target version, any installed copy skips (plain idempotency).
	//     Uninstall of an absent app is always skipped.
	if key := strings.TrimSpace(spec.UninstallKey); key != "" || strings.TrimSpace(spec.DisplayName) != "" {
		app := softdist.FindInstalledApp(key, spec.DisplayName)
		if action == "install" && app != nil {
			target := strings.TrimSpace(spec.Version)
			// Skip only when we can CONFIRM the installed copy already satisfies the
			// request: no target version (plain idempotency), or a known installed
			// version that is the same or newer. If a target is set but the installed
			// version is unknown or older, proceed (upgrade / ensure the version) — never
			// silently skip a requested upgrade just because the version can't be read.
			skip := target == "" ||
				(app.DisplayVersion != "" && softdist.CompareVersions(app.DisplayVersion, target) >= 0)
			if skip {
				reason := "already installed (skipped)"
				if app.DisplayVersion != "" {
					reason = "already installed (version " + app.DisplayVersion + ", skipped)"
				}
				slog.Info("software_deploy: app already installed, skipping",
					"command_id", cmdID, "app", app.DisplayName, "installed_version", app.DisplayVersion, "target_version", target)
				send(ctx, s.outbox, actionStatusMsg(simpleActionStatus(cmdID, actionSoftwareDeploy, s.deviceID, "",
					pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE, reason)))
				return
			}
			slog.Info("software_deploy: proceeding as upgrade/ensure-version",
				"command_id", cmdID, "app", app.DisplayName, "installed", app.DisplayVersion, "target", target)
		}
		if action == "uninstall" && app == nil {
			slog.Info("software_deploy: app not installed, nothing to uninstall", "command_id", cmdID, "key", key, "display_name", spec.DisplayName)
			send(ctx, s.outbox, actionStatusMsg(simpleActionStatus(cmdID, actionSoftwareDeploy, s.deviceID, "",
				pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE, "not installed (nothing to uninstall)")))
			return
		}
	}

	// 0b. Uninstall by MSI ProductCode needs NO installer file — Windows Installer
	//     removes the product from its own database (msiexec /x {GUID}). Reaching here
	//     on an uninstall with a product_code means the product IS installed (STEP 0
	//     short-circuits the absent case), so enqueue a fileless uninstall.
	if action == "uninstall" && strings.TrimSpace(spec.ProductCode) != "" && isMSIType(spec) {
		slog.Info("software_deploy: uninstall by product_code (no installer file needed)", "command_id", cmdID, "product_code", spec.ProductCode)
		s.enqueue(ctx, softdistJob{commandID: cmdID, spec: spec})
		return
	}

	// 0c. Uninstall a NON-MSI app from the registry Uninstall hive needs NO installer
	//     file either — the agent runs the vendor's registered (Quiet)UninstallString.
	//     Reaching here on an uninstall with a key/display_name means the app IS
	//     installed (0a short-circuits the absent case), so enqueue a fileless job.
	if action == "uninstall" && (strings.TrimSpace(spec.UninstallKey) != "" || strings.TrimSpace(spec.DisplayName) != "") {
		slog.Info("software_deploy: uninstall by registry entry (no installer file needed)", "command_id", cmdID, "key", spec.UninstallKey, "display_name", spec.DisplayName)
		s.enqueue(ctx, softdistJob{commandID: cmdID, spec: spec})
		return
	}

	// 0d. Generic "command" type runs an arbitrary command (file_path) directly — no
	//     installer file to deliver or verify. Enqueue it straight to the worker.
	if strings.EqualFold(strings.TrimSpace(spec.InstallerType), "command") {
		if strings.TrimSpace(spec.FilePath) == "" {
			send(ctx, s.outbox, actionStatusMsg(simpleActionStatus(cmdID, actionSoftwareDeploy, s.deviceID, "",
				pb.ActionPhase_ACTION_PHASE_FAILED, `installer_type "command" needs file_path set to the command to run`)))
			return
		}
		slog.Info("software_deploy: generic command (no file delivery)", "command_id", cmdID, "command", spec.FilePath)
		s.enqueue(ctx, softdistJob{commandID: cmdID, spec: spec})
		return
	}

	// 0e. SUPPLY-CHAIN GATE: every file-based payload must be hash-pinned in the
	//     signed command (BigFix prefetch model). The download folder is untrusted
	//     staging — anyone/anything can drop a file there under the expected name, so
	//     the file is trusted only if it matches the sha pinned in the Ed25519-signed
	//     spec. No bypass: an unpinned file-based deploy is refused outright.
	//     (Fileless uninstalls and the "command" type carry no file — they returned
	//     above; the signed command text itself is their integrity.)
	if strings.TrimSpace(spec.ExpectedHash) == "" {
		slog.Error("software_deploy: refusing unpinned file-based payload (no expected_hash)", "command_id", cmdID, "name", spec.Name)
		send(ctx, s.outbox, actionStatusMsg(simpleActionStatus(cmdID, actionSoftwareDeploy, s.deviceID, "",
			pb.ActionPhase_ACTION_PHASE_FAILED,
			"unverified payload refused — expected_hash (sha256) is required for file-based deploys; the console computes it at package upload")))
		return
	}

	// 1. Pre-staged/local file wins (an admin-dropped file in SKVC\download, or an
	//    explicit absolute path). A bare name resolves under the controlled download dir.
	if fp := strings.TrimSpace(spec.FilePath); fp != "" {
		local := s.resolveLocal(fp)
		if fileExists(local) {
			slog.Info("software_deploy: using pre-staged local file", "command_id", cmdID, "file", local)
			s.enqueue(ctx, softdistJob{commandID: cmdID, spec: spec, localPath: local})
			return
		}
		slog.Warn("software_deploy: pre-staged file not found, trying server delivery", "command_id", cmdID, "looked_at", local)
	}

	// 2. Server-delivered: need a content_id to fetch.
	id := strings.TrimSpace(spec.ContentId)
	if id == "" {
		send(ctx, s.outbox, actionStatusMsg(simpleActionStatus(cmdID, actionSoftwareDeploy, s.deviceID, "",
			pb.ActionPhase_ACTION_PHASE_FAILED, "no installer: set file_path (pre-staged) or content_id (server delivery)")))
		return
	}
	if s.cache == nil {
		send(ctx, s.outbox, actionStatusMsg(simpleActionStatus(cmdID, actionSoftwareDeploy, s.deviceID, "",
			pb.ActionPhase_ACTION_PHASE_FAILED, "content delivery unavailable (no cache)")))
		return
	}
	if s.cache.Has(id) {
		slog.Info("software_deploy: installer already cached, using it", "command_id", cmdID, "content_id", id)
		s.enqueue(ctx, softdistJob{commandID: cmdID, spec: spec, cacheID: id})
		return
	}

	// 3. Fetch in the background (a large installer must not block the receiver),
	//    verify integrity, store encrypted, then enqueue the install.
	go func() {
		defer recoverLog("softdist-fetch")
		send(ctx, s.outbox, actionStatusMsg(simpleActionStatus(cmdID, actionSoftwareDeploy, s.deviceID, "",
			pb.ActionPhase_ACTION_PHASE_DOWNLOADING, "fetching installer from server")))

		tmp := filepath.Join(s.tmpDir, "softdist-fetch-"+sanitize(cmdID)+installerExt(spec))
		// fetchContent streams over mTLS and verifies SHA-256 after download.
		req := &pb.ContentRequest{Kind: "package", Id: id, Sha256: spec.ExpectedHash}
		if err := fetchContent(ctx, client, req, tmp); err != nil {
			slog.Error("software_deploy: fetch failed", "command_id", cmdID, "content_id", id, "error", err.Error())
			send(ctx, s.outbox, actionStatusMsg(simpleActionStatus(cmdID, actionSoftwareDeploy, s.deviceID, "",
				pb.ActionPhase_ACTION_PHASE_FAILED, "fetch failed: "+err.Error())))
			return
		}
		// Store encrypted at rest; remove the plaintext temp (cache holds the sealed copy).
		err := s.cache.Put(id, tmp, patchcache.StateStaged)
		if rmErr := secure.SecureDelete(tmp); rmErr != nil {
			slog.Warn("software_deploy: could not remove fetch temp", "temp", tmp, "error", rmErr.Error())
		}
		if err != nil {
			slog.Error("software_deploy: cache put failed", "command_id", cmdID, "content_id", id, "error", err.Error())
			send(ctx, s.outbox, actionStatusMsg(simpleActionStatus(cmdID, actionSoftwareDeploy, s.deviceID, "",
				pb.ActionPhase_ACTION_PHASE_FAILED, "cache put failed: "+err.Error())))
			return
		}
		slog.Info("software_deploy: installer fetched + cached (encrypted)", "command_id", cmdID, "content_id", id)
		s.enqueue(ctx, softdistJob{commandID: cmdID, spec: spec, cacheID: id})
	}()
}

// enqueue hands a ready job to the serial worker (or drops it on shutdown).
func (s *softdistInstaller) enqueue(ctx context.Context, job softdistJob) {
	select {
	case s.jobs <- job:
	case <-ctx.Done():
	}
}

// worker runs software deploys ONE AT A TIME. Each job is isolated with safeRun so
// one deploy's panic cannot kill the worker (which would stall every later deploy).
func (s *softdistInstaller) worker() {
	for job := range s.jobs {
		job := job
		safeRun("softdist-deploy", func() { s.run(job) })
	}
}

// run executes one deploy and streams its terminal ActionStatus. A cached
// (server-delivered, encrypted) installer is decrypted to a transient temp that is
// always secure-deleted; a local file_path is run in place.
func (s *softdistInstaller) run(job softdistJob) {
	spec := job.spec
	cmdID := job.commandID
	started := time.Now().Unix()

	sendStatus(s.outbox, &pb.ActionStatus{
		ActionId: cmdID, ActionType: actionSoftwareDeploy, DeviceId: s.deviceID,
		Phase: pb.ActionPhase_ACTION_PHASE_RUNNING, Reason: "starting install", StartedAt: started,
	})

	// One installer at a time across the whole machine (shared with patches).
	machineInstallGate.Lock()
	defer machineInstallGate.Unlock()

	installPath := job.localPath
	if job.cacheID != "" {
		// Decrypt the sealed installer to a transient temp (plaintext exists only
		// here, only during the install) and secure-delete it afterwards.
		tmp := filepath.Join(s.tmpDir, "softdist-install-"+sanitize(cmdID)+s.cache.Ext(job.cacheID))
		if err := s.cache.Decrypt(job.cacheID, tmp); err != nil {
			slog.Error("software_deploy: cache decrypt failed", "command_id", cmdID, "content_id", job.cacheID, "error", err.Error())
			s.finish(cmdID, spec.Name, started, pb.ActionPhase_ACTION_PHASE_FAILED, &softdist.Result{Reason: "cache decrypt failed: " + err.Error()})
			return
		}
		installPath = tmp
		defer func() {
			if err := secure.SecureDelete(tmp); err != nil {
				slog.Warn("software_deploy: could not remove decrypted temp", "temp", tmp, "error", err.Error())
			}
		}()
	}

	// For the generic "command" type the raw command is in file_path (no delivered
	// file); otherwise use the resolved/decrypted installer path.
	filePath := installPath
	if strings.EqualFold(strings.TrimSpace(spec.InstallerType), "command") {
		filePath = spec.FilePath
	}

	res := softdist.Run(context.Background(), softdist.Params{
		Name:             spec.Name,
		Action:           spec.Action,
		InstallerType:    spec.InstallerType,
		FilePath:         filePath,
		SilentArgs:       spec.SilentArgs,
		ExpectedHash:     spec.ExpectedHash,
		HashAlgorithm:    spec.HashAlgorithm,
		SuccessCodes:     spec.SuccessExitCodes,
		Timeout:          time.Duration(spec.TimeoutSeconds) * time.Second,
		RequireSignature: spec.RequireSignature,
		ProductCode:      spec.ProductCode,
		UninstallKey:     spec.UninstallKey,
		DisplayName:      spec.DisplayName,
		Parameters:       s.buildParams(spec),
	})

	var phase pb.ActionPhase
	switch {
	case res.NotApplicable:
		phase = pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE
	case res.Success:
		phase = pb.ActionPhase_ACTION_PHASE_SUCCESS
	default:
		phase = pb.ActionPhase_ACTION_PHASE_FAILED
	}

	// Post-action VERIFICATION (BigFix's "Fixed" determination): an installer's exit
	// code says the process ran, not that the product landed (or left). When the spec
	// carries detection identifiers, re-detect and only then confirm SUCCESS; a
	// contradiction is reported honestly as FAILED. Specs without identifiers (plain
	// scripts) are trusted on exit code alone.
	if phase == pb.ActionPhase_ACTION_PHASE_SUCCESS {
		action := strings.ToLower(strings.TrimSpace(spec.Action))
		if action == "" {
			action = "install"
		}
		if ok, note := verifyOutcome(spec, action); !ok {
			phase = pb.ActionPhase_ACTION_PHASE_FAILED
			res.Reason = fmt.Sprintf("installer exited %d but verification failed: %s", res.ExitCode, note)
			slog.Warn("software_deploy: post-action verification FAILED", "command_id", cmdID, "name", spec.Name, "note", note)
		} else if note != "" {
			res.Reason += " — " + note
		}
	}
	// A server-delivered installer is single-use per machine — drop it from the
	// cache after a successful install (orphans are swept by the cache janitor TTL).
	if job.cacheID != "" && res.Success {
		if err := s.cache.Remove(job.cacheID); err != nil {
			slog.Warn("software_deploy: could not remove installed package from cache", "content_id", job.cacheID, "error", err.Error())
		}
	}
	s.finish(cmdID, spec.Name, started, phase, &res)
}

// finish streams the terminal ActionStatus and logs the outcome.
func (s *softdistInstaller) finish(cmdID, name string, started int64, phase pb.ActionPhase, res *softdist.Result) {
	sendStatus(s.outbox, &pb.ActionStatus{
		ActionId: cmdID, ActionType: actionSoftwareDeploy, DeviceId: s.deviceID,
		Phase: phase, ExitCode: int32(res.ExitCode), RebootRequired: res.RebootRequired,
		Reason: res.Reason, Detail: res.Detail, StartedAt: started, FinishedAt: time.Now().Unix(),
	})
	slog.Info("software_deploy: finished", "command_id", cmdID, "name", name,
		"phase", phase.String(), "exit_code", res.ExitCode, "reboot_required", res.RebootRequired, "reason", res.Reason)
}

// fileExists reports whether path is present on disk.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// buildParams merges the built-in RUNTIME variables (resolved on this endpoint) with
// the author-supplied spec.parameters. Author values override a built-in of the same
// name. These feed ${name} substitution and the AEGIS_PARAM_<NAME> child env vars.
func (s *softdistInstaller) buildParams(spec *pb.SoftwareDeploySpec) map[string]string {
	p := map[string]string{
		"device_id":    s.deviceID,
		"download_dir": s.downloadDir,
		"temp_dir":     s.tmpDir,
		"arch":         runtime.GOARCH,
	}
	if h, err := os.Hostname(); err == nil {
		p["hostname"] = h
	}
	for k, v := range spec.Parameters {
		if k = strings.TrimSpace(k); k != "" {
			p[k] = v
		}
	}
	return p
}

// verifyOutcome re-detects the product after a successful install/uninstall and
// reports whether reality matches the action. It retries a few times because some
// uninstallers (notably NSIS) exit before the removal fully completes, and MSI
// registry entries can lag a moment. When the spec carries no detection identifiers
// there is nothing to check — the exit code stands (ok, no note).
func verifyOutcome(spec *pb.SoftwareDeploySpec, action string) (ok bool, note string) {
	hasIDs := strings.TrimSpace(spec.ProductCode) != "" ||
		strings.TrimSpace(spec.UninstallKey) != "" || strings.TrimSpace(spec.DisplayName) != ""
	if !hasIDs {
		return true, ""
	}
	wantPresent := action != "uninstall"

	var present bool
	var version string
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(5 * time.Second) // settle: NSIS uninstallers finish after exiting
		}
		present, version = detectProduct(spec)
		if present == wantPresent {
			break
		}
	}

	switch {
	case wantPresent && present:
		// Install verified. When a target version was requested, confirm it took.
		target := strings.TrimSpace(spec.Version)
		if target != "" && version != "" && softdist.CompareVersions(version, target) < 0 {
			return false, fmt.Sprintf("product present but still version %s (target %s) — upgrade did not apply", version, target)
		}
		if version != "" {
			return true, "verified installed (version " + version + ")"
		}
		return true, "verified installed"
	case !wantPresent && !present:
		return true, "verified removed"
	case wantPresent:
		return false, "product not detected after install"
	default:
		return false, "product still detected after uninstall"
	}
}

// detectProduct reports whether the spec's product is present (MSI ProductCode or
// registry Uninstall hive) and its registry DisplayVersion when known.
func detectProduct(spec *pb.SoftwareDeploySpec) (present bool, version string) {
	if pc := strings.TrimSpace(spec.ProductCode); pc != "" && softdist.ProductInstalled(pc) {
		present = true
	}
	key, name := strings.TrimSpace(spec.UninstallKey), strings.TrimSpace(spec.DisplayName)
	if key != "" || name != "" {
		if app := softdist.FindInstalledApp(key, name); app != nil {
			return true, app.DisplayVersion
		}
	}
	return present, ""
}

// isMSIType reports whether the deploy targets an MSI product — an explicit "msi"
// installer_type, or "" / "auto" with a ProductCode set (a ProductCode is an MSI
// concept). Used to decide the fileless uninstall-by-ProductCode path.
func isMSIType(spec *pb.SoftwareDeploySpec) bool {
	switch strings.ToLower(strings.TrimSpace(spec.InstallerType)) {
	case "msi":
		return true
	case "", "auto":
		return strings.TrimSpace(spec.ProductCode) != ""
	default:
		return false
	}
}

// installerExt returns the file extension to give the fetched/decrypted installer
// so the right tool runs it: the file_path hint first, else from installer_type.
func installerExt(spec *pb.SoftwareDeploySpec) string {
	if e := filepath.Ext(strings.TrimSpace(spec.FilePath)); e != "" {
		return e
	}
	switch strings.ToLower(strings.TrimSpace(spec.InstallerType)) {
	case "msi":
		return ".msi"
	case "ps1":
		return ".ps1"
	case "bat", "cmd":
		return ".bat"
	case "py", "python":
		return ".py"
	default:
		return ".exe"
	}
}
