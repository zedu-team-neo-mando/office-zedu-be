package test_auth

import (
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"

	"github.com/hngprojects/telex_be/pkg/repository/storage"
	"github.com/hngprojects/telex_be/services/auth"
	"github.com/hngprojects/telex_be/tests"
)

// TestClaimZeduSyncWithoutRedisLetsEveryLoginSync verifies that when Redis
// is unavailable, every login is allowed to sync (fail open).
func TestClaimZeduSyncWithoutRedisLetsEveryLoginSync(t *testing.T) {
	logger := tests.Setup()

	// Database with no Redis client — simulates Redis being unavailable.
	db := &storage.Database{
		Postgresql: nil,
		Redis:      nil,
		IsTest:     true,
	}

	claimed, err := auth.ClaimZeduSync(db, "user-123", "org-456")
	assert.NoError(t, err)
	assert.True(t, claimed, "expected sync to be allowed when Redis is unavailable")

	logger.Info("TestClaimZeduSyncWithoutRedisLetsEveryLoginSync completed")
}

// TestClaimZeduSyncFailsClosedWhenRedisCannotAnswer verifies that when Redis
// returns an error, the claim is denied (fail closed).
func TestClaimZeduSyncFailsClosedWhenRedisCannotAnswer(t *testing.T) {
	logger := tests.Setup()

	// Database with a Redis client pointed at an unreachable address so that
	// any Redis operation fails — simulates Redis being unable to answer.
	unreachableRedis := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  time.Second,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	})
	defer unreachableRedis.Close()

	db := &storage.Database{
		Postgresql: nil,
		Redis:      unreachableRedis,
		IsTest:     true,
	}

	claimed, err := auth.ClaimZeduSync(db, "user-123", "org-456")
	assert.Error(t, err)
	assert.False(t, claimed, "expected sync to be denied when Redis cannot answer")

	logger.Info("TestClaimZeduSyncFailsClosedWhenRedisCannotAnswer completed")
}
