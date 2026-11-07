package test_auth

import (
	"context"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"
	"net/http"
	"strings"
	"testing"

	"github.com/hngprojects/telex_be/external/mocks/zedu_mocks"
	"github.com/hngprojects/telex_be/internal/models"
	authService "github.com/hngprojects/telex_be/services/auth"
	tst "github.com/hngprojects/telex_be/tests"
	"github.com/hngprojects/telex_be/utility"
)

// Two syncs can overlap, so the one holding the older snapshot may reach the
// deactivation pass after the newer one already mirrored a colleague. The stale
// sync's own list says the colleague is gone; the second read it takes before
// deactivating disagrees, which must keep them.
func TestZeduSyncKeepsAColleagueWhoJoinedAfterItsSnapshot(t *testing.T) {
	r, db, deps := zeduLoginRouter(t)
	owner := strings.ToLower(fmt.Sprintf("zedu-race-%s@example.com", utility.RandomString(8)))
	orgID := zedu_mocks.OrgID(owner)

	login := postZeduLogin(r, models.ZeduLoginRequestModel{Token: zedu_mocks.ValidTokenPrefix + owner})
	tst.AssertStatusCode(t, login.Code, http.StatusOK)
	authService.WaitForZeduSyncs()

	colleague := zedu_mocks.ColleagueEmail(owner)
	var colleagueUser models.User
	if err := db.Postgresql.Where("email = ?", colleague).First(&colleagueUser).Error; err != nil {
		t.Fatalf("expected the colleague to be mirrored: %v", err)
	}

	// This sync's member list predates the colleague, as a sync started before
	// they joined would see.
	err := authService.SyncZeduOrg(deps, authService.ZeduSyncJob{
		Token:       zedu_mocks.StaleMemberListTokenPrefix + owner,
		OrgID:       orgID,
		OwnerZeduID: zedu_mocks.UserID(owner),
		CallerID:    zedu_mocks.UserID(owner),
	})
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	var membership models.OrgUserManagement
	if err := db.Postgresql.Where("organisation_id = ? AND user_id = ?", orgID, colleagueUser.ID).First(&membership).Error; err != nil {
		t.Fatalf("expected the colleague's membership: %v", err)
	}
	if membership.IsDeactivated {
		t.Fatal("a sync holding an older snapshot deactivated a colleague the newer sync added")
	}

	var channelRows int64
	db.Postgresql.Model(&models.UserChannels{}).
		Where("channels_id = ? AND user_id = ?", zedu_mocks.GeneralChannelID(owner), colleagueUser.ID).
		Count(&channelRows)
	if channelRows == 0 {
		t.Fatal("the stale sync removed the colleague from a channel they are still in")
	}
}

// Both calls bypass the Redis rate limit, as happens without Redis or when a
// claim expires. A sync must hold the organisation lock until its writes finish.
func TestZeduSyncSerializesOrganisationSnapshots(t *testing.T) {
	r, db, deps := zeduLoginRouter(t)
	email := strings.ToLower(fmt.Sprintf("zedu-serial-%s@example.com", utility.RandomString(8)))
	login := postZeduLogin(r, models.ZeduLoginRequestModel{Token: zedu_mocks.ValidTokenPrefix + email})
	tst.AssertStatusCode(t, login.Code, http.StatusOK)
	authService.WaitForZeduSyncs()

	type syncContextKey struct{}
	olderLocked := make(chan struct{})
	newerAttempted := make(chan struct{})
	newerRead := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce, olderOnce, attemptOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()

	before := "test:zedu_sync_before_lock"
	after := "test:zedu_sync_after_lock"
	if err := db.Postgresql.Callback().Query().Before("gorm:query").Register(before, func(tx *gorm.DB) {
		if tx.Statement.Table == "organisations" && tx.Statement.Context.Value(syncContextKey{}) == "newer" {
			attemptOnce.Do(func() { close(newerAttempted) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Postgresql.Callback().Query().Remove(before)
	if err := db.Postgresql.Callback().Query().After("gorm:query").Register(after, func(tx *gorm.DB) {
		if tx.Error != nil {
			return
		}
		switch tx.Statement.Context.Value(syncContextKey{}) {
		case "older":
			if tx.Statement.Table == "channels" {
				olderOnce.Do(func() { close(olderLocked); <-release })
			}
		case "newer":
			if tx.Statement.Table != "organisations" {
				return
			}
			select {
			case newerRead <- struct{}{}:
			default:
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Postgresql.Callback().Query().Remove(after)

	job := authService.ZeduSyncJob{Token: zedu_mocks.ValidTokenPrefix + email,
		OrgID: zedu_mocks.OrgID(email), OwnerZeduID: zedu_mocks.UserID(email), CallerID: zedu_mocks.UserID(email)}
	results := make(chan error, 2)
	start := func(name string) {
		local := deps
		ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), syncContextKey{}, name), 10*time.Second)
		local.Db = deps.Db.WithContext(ctx)
		go func() { defer cancel(); results <- authService.SyncZeduOrg(local, job) }()
	}
	await := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("sync did not reach the expected database read")
		}
	}
	start("older")
	await(olderLocked)
	start("newer")
	await(newerAttempted)
	select {
	case <-newerRead:
		t.Error("newer sync read a snapshot before the older sync finished")
	case <-time.After(200 * time.Millisecond):
	}
	unblock()
	for range 2 {
		if err := <-results; err != nil {
			t.Errorf("sync failed: %v", err)
		}
	}
	await(newerRead)
}
