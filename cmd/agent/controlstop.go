package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
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
func stopTokenFromArgs(args []string) (string, bool) {
	for i, a := range args {
		switch {
		case a == "--token-prompt" || a == "-token-prompt" || a == "/token-prompt":
			return promptToken(), true
		case strings.HasPrefix(a, "--token=") || strings.HasPrefix(a, "-token="):
			return strings.SplitN(a, "=", 2)[1], true
		case a == "--token" || a == "-token" || a == "/token":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				return args[i+1], true
			}
			return promptToken(), true // flag with no value → prompt
		}
	}
	return "", false
}

func promptToken() string {
	fmt.Print("Enter unprotect token: ")
	s, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(s)
}
