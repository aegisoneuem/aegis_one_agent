package main

import (
	"testing"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

func TestMockWorkflowInputs(t *testing.T) {
	for _, tc := range []struct {
		name               string
		env                map[string]string
		bundle, want, fail bool
	}{
		{"unchanged old deployment", nil, false, false, false},
		{"pre and post", map[string]string{"AEGIS_DEPLOY_PRE_COMMAND": "cmd.exe", "AEGIS_DEPLOY_PRE_ARGS": "/D /C exit 0", "AEGIS_DEPLOY_POST_COMMAND": "cmd.exe", "AEGIS_DEPLOY_POST_SECURE_INPUT": "true"}, true, true, false},
		{"fileless not supported yet", map[string]string{"AEGIS_DEPLOY_PRE_COMMAND": "cmd.exe"}, false, false, true},
		{"orphan args", map[string]string{"AEGIS_DEPLOY_PRE_ARGS": "/D /C exit 0"}, true, false, true},
		{"bad timeout", map[string]string{"AEGIS_DEPLOY_PRE_COMMAND": "cmd.exe", "AEGIS_DEPLOY_PRE_TIMEOUT_SECONDS": "3601"}, true, false, true},
		{"bad success code", map[string]string{"AEGIS_DEPLOY_PRE_COMMAND": "cmd.exe", "AEGIS_DEPLOY_PRE_SUCCESS_CODES": "7,no"}, true, false, true},
		{"bad secure flag", map[string]string{"AEGIS_DEPLOY_PRE_COMMAND": "cmd.exe", "AEGIS_DEPLOY_PRE_SECURE_INPUT": "maybe"}, true, false, true},
		{"secret copied into arguments", map[string]string{"AEGIS_DEPLOY_PRE_COMMAND": "cmd.exe", "AEGIS_DEPLOY_PRE_ARGS": "private-secret"}, true, false, true},
		{"password copied into step name", map[string]string{"AEGIS_DEPLOY_PRE_COMMAND": "cmd.exe", "AEGIS_DEPLOY_PRE_NAME": "account-password"}, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := &pb.SoftwareDeploySpec{SecureParameters: map[string]string{"TOKEN": "private-secret"}, Execution: &pb.SoftwareExecution{PasswordUtf8: []byte("account-password")}}
			if tc.bundle {
				spec.Bundle = &pb.SoftwareBundle{Command: "cmd.exe"}
			}
			got, err := applyMockWorkflow(spec, func(key string) string { return tc.env[key] })
			if got != tc.want || (err != nil) != tc.fail {
				t.Fatalf("got workflow=%v, error=%v", got, err)
			}
			if got && (!spec.Steps.After[0].UseSecureParameters || spec.Steps.Before[0].UseSecureParameters) {
				t.Fatal("secure stdin opt-in ignored")
			}
		})
	}
}
