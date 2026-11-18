package uimsg

import (
	"encoding/json"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	in := Message{Kind: KindPrompt, ID: "p1", Title: "Reboot", Text: "Restart now?", Buttons: []string{"Now", "Later"}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Message
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Kind != KindPrompt || out.ID != "p1" || out.Title != "Reboot" || len(out.Buttons) != 2 {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestNotifyOmitsEmpty(t *testing.T) {
	b, _ := json.Marshal(Message{Kind: KindNotify, Title: "T"})
	// optional fields must be omitted when empty
	if s := string(b); contains(s, "buttons") || contains(s, "\"id\"") {
		t.Fatalf("empty fields not omitted: %s", s)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
