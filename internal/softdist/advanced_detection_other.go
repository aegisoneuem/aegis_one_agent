//go:build !windows

package softdist

import (
	"context"
	"fmt"
)

func EvaluateDetection(context.Context, Detection, string, bool) (DetectionResult, error) {
	return DetectionResult{}, fmt.Errorf("advanced software detection requires Windows")
}
