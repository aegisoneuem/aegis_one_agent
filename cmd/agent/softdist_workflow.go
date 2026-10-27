package main

import (
	"fmt"

	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

func deploymentSteps(spec *pb.SoftwareDeploySpec) softdist.DeploymentSteps {
	convert := func(list []*pb.SoftwareCommandStep) []softdist.CommandStep {
		out := make([]softdist.CommandStep, 0, len(list))
		for _, step := range list {
			out = append(out, softdist.CommandStep{Name: step.GetName(), Command: step.GetCommand(), Arguments: step.GetArguments(), TimeoutSeconds: step.GetTimeoutSeconds(), SuccessCodes: step.GetSuccessExitCodes(), UseSecureParameters: step.GetUseSecureParameters()})
		}
		return out
	}
	return softdist.DeploymentSteps{Before: convert(spec.GetSteps().GetBefore()), After: convert(spec.GetSteps().GetAfter())}
}

func validateWorkflowDeployment(spec *pb.SoftwareDeploySpec) error {
	if spec.Steps == nil || spec.Bundle == nil {
		return fmt.Errorf("software_deploy_workflow requires steps and a verified bundle")
	}
	return deploymentSteps(spec).Validate()
}
