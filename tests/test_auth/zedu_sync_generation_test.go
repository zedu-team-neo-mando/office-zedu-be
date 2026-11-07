package test_auth

import (
	"testing"

	"github.com/hngprojects/telex_be/pkg/repository/storage"
	authService "github.com/hngprojects/telex_be/services/auth"
	tst "github.com/hngprojects/telex_be/tests"
	"github.com/hngprojects/telex_be/utility"
)

// An older sync that is still running when a newer one starts must see that it
// has been overtaken, so it leaves its removals to the newer sync.
func TestZeduSyncGenerationFencesAnOlderSync(t *testing.T) {
	tst.Setup()
	client := storage.Connection().Redis
	if client == nil {
		t.Skip("no Redis in this environment")
	}
	orgID := utility.GenerateUUID()

	older, err := authService.BeginZeduSync(client, orgID)
	if err != nil {
		t.Skipf("Redis is unavailable: %v", err)
	}
	newer, err := authService.BeginZeduSync(client, orgID)
	if err != nil {
		t.Fatalf("starting the newer sync: %v", err)
	}

	if superseded, err := authService.ZeduSyncSuperseded(client, orgID, older); err != nil || !superseded {
		t.Errorf("the older sync should be superseded, got %v (err %v)", superseded, err)
	}
	if superseded, err := authService.ZeduSyncSuperseded(client, orgID, newer); err != nil || superseded {
		t.Errorf("the newer sync should not be superseded, got %v (err %v)", superseded, err)
	}
}

// Without Redis there is no counter to compare, so every sync keeps its
// removals, as before generations existed.
func TestZeduSyncWithoutRedisIsNeverSuperseded(t *testing.T) {
	generation, err := authService.BeginZeduSync(nil, "any-org")
	if err != nil || generation != 0 {
		t.Fatalf("expected generation 0 without Redis, got %d (err %v)", generation, err)
	}
	if superseded, err := authService.ZeduSyncSuperseded(nil, "any-org", generation); err != nil || superseded {
		t.Errorf("a sync without Redis must not be superseded, got %v (err %v)", superseded, err)
	}
}
