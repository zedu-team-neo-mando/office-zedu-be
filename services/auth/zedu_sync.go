package auth

import (
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"

	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	rd "github.com/hngprojects/telex_be/pkg/repository/storage/redis"
	"github.com/hngprojects/telex_be/utility"
)

// ZeduSyncJob represents a job to sync Zedu data for an organisation.
type ZeduSyncJob struct {
	OrgID string `json:"org_id"`
}

// ClaimZeduSync checks if a user can claim Zedu sync for an organisation.
// It uses Redis to track whether sync has already been claimed.
//
// Behaviour:
//   - If Redis is unavailable (nil client), every login is allowed to sync
//     (fail open).
//   - If Redis returns an error when queried, the claim is denied (fail closed).
//   - If the sync has already been claimed for this user+org, it is denied.
//   - Otherwise, the claim is recorded in Redis and allowed.
func ClaimZeduSync(db *storage.Database, userID, orgID string) (bool, error) {
	// If Redis is unavailable, let every login sync (fail open).
	if db == nil || db.Redis == nil {
		return true, nil
	}

	key := fmt.Sprintf("zedu_sync:claimed:%s:%s", orgID, userID)

	// Check whether the sync has already been claimed.
	_, err := rd.RedisGet(db.Redis, key)
	if err != nil && err != redis.Nil {
		// Redis could not answer — fail closed.
		return false, fmt.Errorf("failed to check zedu sync claim status: %w", err)
	}
	if err == nil {
		// Key exists — sync already claimed.
		return false, nil
	}

	// Record the claim in Redis with a 24-hour TTL.
	ttl := 24 * time.Hour
	if err := rd.RedisSet(db.Redis, key, true, ttl); err != nil {
		return false, fmt.Errorf("failed to record zedu sync claim: %w", err)
	}

	return true, nil
}

// deactivateDeparted deactivates users who have departed from an organisation.
// It uses job.OrgID for the organisation filter and takes at most three
// positional parameters: the job, the database handle, and the logger.
func deactivateDeparted(job ZeduSyncJob, db *storage.Database, logger *utility.Logger) error {
	orgID := job.OrgID

	if db == nil || db.Postgresql == nil {
		return fmt.Errorf("database connection is required")
	}

	// Find organisation user management records for this org that are active
	// but should be deactivated (departed members).
	var departed []struct {
		UserID         string
		OrganisationID string
	}
	if err := db.Postgresql.Table("org_user_managements").
		Select("user_id, organisation_id").
		Where("organisation_id = ? AND is_deactivated = ? AND status = ?", orgID, false, "active").
		Scan(&departed).Error; err != nil {
		return fmt.Errorf("failed to fetch departed members for org %s: %w", orgID, err)
	}

	for _, member := range departed {
		user := &models.User{}
		if err := user.DeactivateOrgMember(db.Postgresql, member.OrganisationID, logger); err != nil {
			logger.Warning("failed to deactivate departed user %s in org %s: %v", member.UserID, orgID, err)
			continue
		}
		logger.Info("deactivated departed user %s in org %s", member.UserID, orgID)
	}

	return nil
}