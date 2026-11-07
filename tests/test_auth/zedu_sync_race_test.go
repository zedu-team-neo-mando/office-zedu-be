package test_auth

import (
	"fmt"
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
