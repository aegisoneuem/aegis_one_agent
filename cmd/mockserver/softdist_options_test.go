package main

import "testing"

func TestSoftdistTestControls(t *testing.T) {
	values := map[string]string{"AEGIS_DEPLOY_AUTO_HASH": "0", "AEGIS_DEPLOY_TIMEOUT": "2", "AEGIS_DEPLOY_SUCCESS_CODES": "19, 42"}
	lookup := func(k string) string { return values[k] }
	o, err := readSoftdistTestOptions(lookup)
	if err != nil || o.autoHash || o.timeout != 2 || len(o.successCodes) != 2 || o.successCodes[0] != 19 {
		t.Fatalf("%+v %v", o, err)
	}
	for key, value := range map[string]string{"AEGIS_DEPLOY_AUTO_HASH": "bad", "AEGIS_DEPLOY_TIMEOUT": "-1", "AEGIS_DEPLOY_SUCCESS_CODES": "hello"} {
		old := values[key]
		values[key] = value
		if _, err := readSoftdistTestOptions(lookup); err == nil {
			t.Errorf("accepted %s=%s", key, value)
		}
		values[key] = old
	}
}
