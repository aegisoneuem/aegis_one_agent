package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/config"
)

// tokenStop asks the RUNNING agent (via its localhost control endpoint) to stop,
// presenting a server-signed unprotect token. This is the local, non-server stop
// path for a self-protected service: `AegisOneClient.exe stop --token <TOKEN>`
// (or `--token` alone / `--token-prompt` to be prompted). The agent verifies the
// token before stopping, so an admin without a valid token cannot stop it this way.
func tokenStop(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("no token provided")
	}
	addr := config.Load().HealthAddr
	if strings.TrimSpace(addr) == "" {
		addr = "127.0.0.1:50080"
	}
	url := "http://" + addr + "/control/stop"
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "text/plain", strings.NewReader(token))
	if err != nil {
		return fmt.Errorf("could not reach the agent control endpoint at %s: %w", addr, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(body))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent %s", msg)
	}
	fmt.Println(msg)
	return nil
}

// stopTokenFromArgs extracts the token for a `stop` control command: --token <V>,
// --token=<V>, or an empty/-token-prompt which reads one line from stdin. Returns
// ("", false) when no token mode was requested (caller falls back to SCM stop).
// Accepts either a short passcode (--passcode/-p) or the long token (--token);
// both post the same body to /control/stop, which tries passcode then token.
func stopTokenFromArgs(args []string) (string, bool) {
	for i, a := range args {
		switch {
		case a == "--token-prompt" || a == "-token-prompt" || a == "/token-prompt" ||
			a == "--passcode-prompt" || a == "-passcode-prompt" || a == "/passcode-prompt":
			return promptSecret(), true
		case strings.HasPrefix(a, "--token=") || strings.HasPrefix(a, "-token=") ||
			strings.HasPrefix(a, "--passcode=") || strings.HasPrefix(a, "-passcode=") || strings.HasPrefix(a, "-p="):
			return strings.SplitN(a, "=", 2)[1], true
		case a == "--token" || a == "-token" || a == "/token" ||
			a == "--passcode" || a == "-passcode" || a == "/passcode" || a == "-p":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				return args[i+1], true
			}
			return promptSecret(), true // flag with no value → prompt
		}
	}
	return "", false
}

// uiLevelFromArgs reads --ui=<n> (the MSI UILevel) from the args; 0 if absent.
func uiLevelFromArgs(args []string) int {
	for _, a := range args {
		if strings.HasPrefix(a, "--ui=") {
			n, _ := strconv.Atoi(strings.TrimPrefix(a, "--ui="))
			return n
		}
	}
	return 0
}

func promptSecret() string {
	fmt.Print("Enter unprotect passcode/token: ")
	s, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(s)
}

// uninstallCheck is invoked by the MSI's uninstall custom action
// (AegisOneClient.exe uninstall-check --passcode=<code>). It returns nil (exit 0 =
// allow) when the agent is NOT tamper-protected or a valid passcode/token was given,
// and an error (non-zero = abort the uninstall) otherwise. A major-upgrade removal
// skips this (the MSI condition), so agent upgrades are never blocked.
func uninstallCheck(args []string) error {
	cfg := config.Load()
	loadStoredPasscode(cfg.SecureDir, nil, cfg.DeviceID) // nil outbox: CLI, no server
	if !passcodeConfigured() && !readUnprotectMarker() {
		return nil // not protected → allow uninstall
	}
	secret, _ := stopTokenFromArgs(args)
	_ = loadCommandVerifyKey(cfg) // best-effort: also allow a signed token

	authorized := false
	if secret != "" {
		if ok, _ := authorizeUnprotect(secret, cfg.DeviceID); ok {
			authorized = true
		}
	}
	// Interactive uninstall (Add/Remove Programs) with no/invalid passcode: pop a
	// native passcode prompt, up to 3 tries. Silent (/qn) never prompts — it uses the
	// UNINSTALLPASSCODE property. uiLevel: full=5 reduced=4 basic=3 none=2.
	if !authorized && uiLevelFromArgs(args) >= 4 {
		for i := 0; i < 3 && !authorized; i++ {
			code, ok := promptPasswordDialog("AegisOne Client — Uninstall",
				"This agent is tamper-protected. Enter the unprotect passcode to uninstall:")
			if !ok {
				return fmt.Errorf("uninstall cancelled")
			}
			if ok2, _ := authorizeUnprotect(code, cfg.DeviceID); ok2 {
				secret, authorized = code, true
			}
		}
	}
	if !authorized {
		return fmt.Errorf("uninstall is protected — valid passcode required. Use: msiexec /x AegisOneClient.msi UNINSTALLPASSCODE=<code>")
	}
	// Authorized: STOP the running agent first. A self-protected service does NOT
	// accept the SCM stop, so the MSI's own StopServices would fail (1052) and leave
	// the process/service/files behind. We stop it via the local control endpoint
	// (token/passcode authorized) and wait for it to exit, so the MSI can then
	// delete the service + remove the files cleanly.
	addr := strings.TrimSpace(cfg.HealthAddr)
	if addr == "" {
		addr = "127.0.0.1:50080"
	}
	_ = tokenStop(secret)               // best-effort (agent may already be stopped)
	waitForAgentGone(addr, 20*time.Second)
	return nil
}

// waitForAgentGone polls the agent's local health endpoint until it stops
// responding (the agent has exited) or the timeout elapses.
func waitForAgentGone(addr string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	url := "http://" + addr + "/health"
	client := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err != nil {
			return // endpoint down → agent stopped
		}
		resp.Body.Close()
		time.Sleep(300 * time.Millisecond)
	}
}
