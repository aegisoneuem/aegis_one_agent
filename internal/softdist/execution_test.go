package softdist

import "testing"

func TestExecutionPolicy(t *testing.T) {
	id := uint32(2)
	for _, e := range []Execution{
		{Mode: "invalid"}, {Mode: "system", Username: "bob"}, {Mode: "system", DetectionScope: "user"},
		{Mode: "logged_in_user", Password: []byte("x")}, {Mode: "specified_account", Username: "bob"},
		{Mode: "specified_account", Username: "bob", Password: []byte("x"), SessionID: &id},
		{Mode: "system", DetectionScope: "any"},
	} {
		if e.Validate() == nil {
			t.Errorf("accepted invalid mode %s", e.Mode)
		}
	}
	for _, e := range []Execution{{}, {Mode: "system"}, {Mode: "logged_in_user"}, {Mode: "logged_in_user", SessionID: &id}, {Mode: "specified_account", Username: "bob", Password: []byte("x"), DetectionScope: "user"}} {
		if err := e.Validate(); err != nil {
			t.Errorf("valid execution: %v", err)
		}
	}
}
func TestPrivateParameterPolicy(t *testing.T) {
	for _, p := range []Params{
		{SecureParameters: map[string]string{"BAD-NAME": "value"}},
		{SecureParameters: map[string]string{"TOKEN": "a", "token": "b"}},
		{SecureParameters: map[string]string{"TOKEN": "hidden"}, Parameters: map[string]string{"token": "public"}},
		{SecureParameters: map[string]string{"TOKEN": "hidden"}, SilentArgs: "--token=${TOKEN}"},
		{SecureParameters: map[string]string{"TOKEN": "hidden"}, Parameters: map[string]string{"other": "${TOKEN}"}},
		{SecureParameters: map[string]string{"TOKEN": "hidden"}, Name: "hidden"},
		{SecureParameters: map[string]string{"TOKEN": "hidden"}, Version: "hidden"},
		{SecureParameters: map[string]string{"TOKEN": "hidden"}, Parameters: map[string]string{"visible": "hidden"}},
		{SecureParameters: map[string]string{"TOKEN": "hidden"}, MaxSecretBytes: 2},
	} {
		if ValidatePrivateInput(p) == nil {
			t.Fatal("accepted exposed or invalid private input")
		}
	}
	if err := ValidatePrivateInput(Params{SecureParameters: map[string]string{"API_TOKEN": "private-value"}, Parameters: map[string]string{"REGION": "apac"}, SilentArgs: "--region=${REGION}"}); err != nil {
		t.Fatal(err)
	}
}
