package test_auth

import (
	"testing"
	"time"

	"github.com/go-redis/redis/v8"

	authService "github.com/hngprojects/telex_be/services/auth"
)

// A deployment without Redis has no way to coordinate syncs, and its documented
// behaviour is to sync on every login.
func TestClaimZeduSyncWithoutRedisLetsEveryLoginSync(t *testing.T) {
	claimed, err := authService.ClaimZeduSync(nil, "org-1")
	if err != nil {
		t.Fatalf("no Redis should not be an error: %v", err)
	}
	if !claimed {
		t.Fatal("without Redis every login should start a sync")
	}
}

// A claim that cannot be read must not be treated as granted: two overlapping
// syncs can apply an older member list after a newer one, deactivating a
// colleague the newer sync just added.
func TestClaimZeduSyncFailsClosedWhenRedisCannotAnswer(t *testing.T) {
	// Nothing listens on port 1, so the claim cannot be read.
	client := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1",
		DialTimeout: 100 * time.Millisecond,
		MaxRetries:  0,
	})
	defer client.Close()

	claimed, err := authService.ClaimZeduSync(client, "org-1")
	if err == nil {
		t.Fatal("expected the failed claim to be reported")
	}
	if claimed {
		t.Fatal("a claim that could not be read must not start a sync")
	}
}