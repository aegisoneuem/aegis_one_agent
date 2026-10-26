package main

import (
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"testing"
)

func TestPrivateSettingsRequireV2(t *testing.T) {
	for _, tc := range []struct {
		env    map[string]string
		want   string
		failed bool
	}{
		{map[string]string{}, "software_deploy", false},
		{map[string]string{"AEGIS_DEPLOY_RUN_AS": "system"}, "software_deploy_v2", false},
		{map[string]string{"AEGIS_DEPLOY_RUN_AS": "logged_in_user", "AEGIS_DEPLOY_SESSION_ID": "3"}, "software_deploy_v2", false},
		{map[string]string{"AEGIS_DEPLOY_RUN_AS": "specified_account"}, "", true},
		{map[string]string{"AEGIS_DEPLOY_RUN_AS": "system", "AEGIS_DEPLOY_DETECTION_SCOPE": "user"}, "", true},
		{map[string]string{"AEGIS_DEPLOY_SESSION_ID": "not-a-number"}, "", true},
		{map[string]string{"AEGIS_DEPLOY_SECRETS_FILE": "missing-file.dpapi"}, "", true},
	} {
		spec := &pb.SoftwareDeploySpec{}
		got, err := applyPrivateDeployment(spec, func(key string) string { return tc.env[key] })
		if (err != nil) != tc.failed || got != tc.want {
			t.Fatalf("type=%q error=%v", got, err)
		}
	}
}
