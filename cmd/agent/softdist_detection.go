package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/deployspec"
	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

func advancedDetection(spec *pb.SoftwareDeploySpec, params map[string]string, session *softdist.Session) (softdist.DetectionResult, error) {
	d, err := deployspec.Detection(spec.Detection).Resolve(params)
	if err != nil {
		return softdist.DetectionResult{}, err
	}
	sid := ""
	if session != nil {
		sid = session.DetectionSID()
	}
	return softdist.EvaluateDetection(context.Background(), d, sid, strings.EqualFold(strings.TrimSpace(spec.Action), "uninstall"))
}

func (s *softdistInstaller) advancedPrecheck(spec *pb.SoftwareDeploySpec, session *softdist.Session) (bool, string, error) {
	result, err := advancedDetection(spec, s.buildParams(spec), session)
	if err != nil {
		return false, "", err
	}
	if strings.EqualFold(strings.TrimSpace(spec.Action), "uninstall") {
		return !result.Present, "no selected artifacts present (nothing to uninstall)", nil
	}
	return result.Satisfied, "advanced installed detection satisfied (skipped)", nil
}

func verifyAdvancedOutcome(ctx context.Context, spec *pb.SoftwareDeploySpec, params map[string]string, session *softdist.Session, attempts int, interval time.Duration) (bool, string) {
	if attempts < 1 {
		attempts = 1
	}
	uninstall := strings.EqualFold(strings.TrimSpace(spec.Action), "uninstall")
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return false, "verification cancelled"
		}
		if attempt > 0 {
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return false, "verification cancelled"
			case <-timer.C:
			}
		}
		result, err := advancedDetection(spec, params, session)
		if err != nil {
			return false, fmt.Sprintf("advanced detection failed: %v", err)
		}
		if uninstall && !result.Present {
			return true, "verified selected artifacts removed"
		}
		if !uninstall && result.Satisfied {
			return true, "verified advanced installed detection"
		}
	}
	if uninstall {
		return false, "selected artifacts still present after uninstall"
	}
	return false, "advanced installed detection not satisfied after install"
}
