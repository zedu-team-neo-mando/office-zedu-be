package test_auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hngprojects/telex_be/external/thirdparty/zedu"
)

// zeduBe serves the two answers FetchMe reads, shaped like api.zedu.chat's,
// and fails the test if anything else is asked for.
func zeduBe(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer session-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/profile":
			_, _ = w.Write([]byte(`{"status":"success","data":{"id":"profile-1","user_id":"user-1",` +
				`"email":"Ada@Example.com","username":"ada","full_name":"Ada Lovelace",` +
				`"avatar_url":"https://cdn.example/ada.png","organisation_id":"org-1"}}`))
		case "/organisations/org-1":
			_, _ = w.Write([]byte(`{"status":"success","data":{"id":"org-1","name":"Analytical",` +
				`"email":"team@example.com","type":"team","country":"uk","owner_id":"user-9"}}`))
		default:
			t.Errorf("unexpected zedu-be request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestZeduFetchMeReadsProfileAndOrganisation(t *testing.T) {
	server := zeduBe(t)
	defer server.Close()

	user, err := zedu.Client{BaseUrl: server.URL}.FetchMe("session-token")
	if err != nil {
		t.Fatalf("FetchMe: %v", err)
	}

	if user.ID != "user-1" {
		t.Errorf("the user ID must come from user_id, not the profile id; got %q", user.ID)
	}
	if user.Email != "Ada@Example.com" || user.Username != "ada" || user.Fullname != "Ada Lovelace" {
		t.Errorf("identity not read from /profile: %+v", user)
	}
	if user.CurrentOrg != "org-1" || user.Organisation.ID != "org-1" {
		t.Errorf("organisation not resolved: current %q, organisation %q", user.CurrentOrg, user.Organisation.ID)
	}
	if user.Organisation.OwnerID != "user-9" || user.Organisation.Name != "Analytical" {
		t.Errorf("organisation details not read: %+v", user.Organisation)
	}
}

func TestZeduFetchMeRejectsAnExpiredSession(t *testing.T) {
	server := zeduBe(t)
	defer server.Close()

	if _, err := (zedu.Client{BaseUrl: server.URL}).FetchMe("expired"); err == nil {
		t.Fatal("an expired zedu session must be rejected")
	}
}
