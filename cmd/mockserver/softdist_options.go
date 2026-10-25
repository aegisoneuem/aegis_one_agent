package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Test controls only; production sends these existing SoftwareDeploySpec fields
// from the package definition. No agent/protobuf contract is added here.
type softdistTestOptions struct {
	autoHash     bool
	timeout      int32
	successCodes []int32
}

func readSoftdistTestOptions(lookup func(string) string) (softdistTestOptions, error) {
	o := softdistTestOptions{autoHash: true}
	if v := strings.TrimSpace(lookup("AEGIS_DEPLOY_AUTO_HASH")); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			o.autoHash = true
		case "0", "false", "no", "off":
			o.autoHash = false
		default:
			return o, fmt.Errorf("AEGIS_DEPLOY_AUTO_HASH must be true or false")
		}
	}
	if v := strings.TrimSpace(lookup("AEGIS_DEPLOY_TIMEOUT")); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 0 {
			return o, fmt.Errorf("AEGIS_DEPLOY_TIMEOUT must be nonnegative seconds")
		}
		o.timeout = int32(n)
	}
	if v := strings.TrimSpace(lookup("AEGIS_DEPLOY_SUCCESS_CODES")); v != "" {
		for _, token := range strings.Split(v, ",") {
			n, err := strconv.ParseInt(strings.TrimSpace(token), 10, 32)
			if err != nil {
				return o, fmt.Errorf("invalid AEGIS_DEPLOY_SUCCESS_CODES entry %q", token)
			}
			o.successCodes = append(o.successCodes, int32(n))
		}
	}
	return o, nil
}
