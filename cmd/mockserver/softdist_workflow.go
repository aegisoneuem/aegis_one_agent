package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// The mock exposes one before/after command for readable manual cases. The
// production protocol supports eight in each stage; no JSON test file is needed.
func applyMockWorkflow(spec *pb.SoftwareDeploySpec, lookup func(string) string) (bool, error) {
	steps := &pb.SoftwareDeploymentSteps{}
	native := softdist.DeploymentSteps{}
	for _, stage := range []string{"PRE", "POST", "ROLLBACK"} {
		prefix := "AEGIS_DEPLOY_" + stage + "_"
		command, args, name := lookup(prefix+"COMMAND"), lookup(prefix+"ARGS"), lookup(prefix+"NAME")
		timeout, codes, secure := lookup(prefix+"TIMEOUT_SECONDS"), lookup(prefix+"SUCCESS_CODES"), lookup(prefix+"SECURE_INPUT")
		if command == "" && args == "" && name == "" && timeout == "" && codes == "" && secure == "" {
			continue
		}
		if strings.TrimSpace(command) == "" {
			return false, fmt.Errorf("%sCOMMAND is required when step settings are present", prefix)
		}
		if name == "" {
			name = strings.ToLower(stage) + " deployment command"
		}
		step := &pb.SoftwareCommandStep{Name: name, Command: command, Arguments: args}
		if timeout != "" {
			n, err := strconv.ParseInt(strings.TrimSpace(timeout), 10, 32)
			if err != nil {
				return false, fmt.Errorf("invalid step timeout")
			}
			step.TimeoutSeconds = int32(n)
		}
		if codes != "" {
			for _, value := range strings.Split(codes, ",") {
				n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
				if err != nil {
					return false, fmt.Errorf("invalid step success code")
				}
				step.SuccessExitCodes = append(step.SuccessExitCodes, int32(n))
			}
		}
		if secure != "" {
			switch strings.ToLower(strings.TrimSpace(secure)) {
			case "1", "true":
				step.UseSecureParameters = true
			case "0", "false":
			default:
				return false, fmt.Errorf("step SECURE_INPUT must be 0/1 or false/true")
			}
		}
		commandStep := softdist.CommandStep{Name: name, Command: command, Arguments: args, TimeoutSeconds: step.TimeoutSeconds, SuccessCodes: step.SuccessExitCodes, UseSecureParameters: step.UseSecureParameters}
		switch stage {
		case "PRE":
			steps.Before = append(steps.Before, step)
			native.Before = append(native.Before, commandStep)
		case "POST":
			steps.After = append(steps.After, step)
			native.After = append(native.After, commandStep)
		case "ROLLBACK":
			steps.Rollback = append(steps.Rollback, step)
			native.Rollback = append(native.Rollback, commandStep)
		}
	}
	if len(native.Before)+len(native.After)+len(native.Rollback) == 0 {
		return false, nil
	}
	if spec.Bundle == nil {
		return false, fmt.Errorf("deployment workflow currently requires a bundle")
	}
	if err := native.Validate(); err != nil {
		return false, err
	}
	p := softdist.Params{Parameters: spec.Parameters, SecureParameters: spec.SecureParameters, PublicMetadata: native.PublicFields()}
	if err := softdist.ValidatePrivateInput(p); err != nil {
		return false, err
	}
	if password := spec.GetExecution().GetPasswordUtf8(); len(password) > 0 {
		p.SecureParameters = map[string]string{"RUN_AS_PASSWORD": string(password)}
		if err := softdist.ValidatePrivateInput(p); err != nil {
			return false, fmt.Errorf("account password must not appear in deployment steps")
		}
	}
	spec.Steps = steps
	return true, nil
}
