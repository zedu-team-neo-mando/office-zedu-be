package external_models

import (
	"encoding/json"
	"testing"
)

func TestZeduMeResponseAcceptsNestedUser(t *testing.T) {
	var response ZeduMeResponse
	err := json.Unmarshal([]byte(`{"data":{"user":{"id":"u1","email":"person@example.com"}}}`), &response)
	if err != nil {
		t.Fatalf("unmarshal nested response: %v", err)
	}
	if got := response.Data.User.Email; got != "person@example.com" {
		t.Fatalf("expected nested email, got %q", got)
	}
}

func TestZeduMeResponseAcceptsFlatUser(t *testing.T) {
	var response ZeduMeResponse
	err := json.Unmarshal([]byte(`{"data":{"id":"u1","email":"person@example.com"}}`), &response)
	if err != nil {
		t.Fatalf("unmarshal flat response: %v", err)
	}
	if got := response.Data.User.Email; got != "person@example.com" {
		t.Fatalf("expected flat email, got %q", got)
	}
}

func TestZeduMeResponseRejectsMissingData(t *testing.T) {
	for _, payload := range []string{`{}`, `{"data":null}`} {
		var response ZeduMeResponse
		if err := json.Unmarshal([]byte(payload), &response); err == nil {
			t.Fatalf("expected missing data to be rejected for %s", payload)
		}
	}
}
