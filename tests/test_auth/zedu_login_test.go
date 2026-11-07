package test_auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"

	"github.com/hngprojects/telex_be/external/external_models"
	"github.com/hngprojects/telex_be/external/mocks/zedu_mocks"
	"github.com/hngprojects/telex_be/external/request"
	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/controller/auth"
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	authService "github.com/hngprojects/telex_be/services/auth"
	tst "github.com/hngprojects/telex_be/tests"
	"github.com/hngprojects/telex_be/utility"
)

func zeduLoginRouter(t *testing.T) (*gin.Engine, *storage.Database, authService.ZeduLoginDeps) {
	t.Helper()
	logger := tst.Setup()
	gin.SetMode(gin.TestMode)

	db := storage.Connection()
	extReq := request.ExternalRequest{Logger: logger, Test: true}
	controller := auth.Controller{Db: db, Validator: validator.New(), Logger: logger, ExtReq: extReq}

	r := gin.Default()
	r.POST("/api/v1/auth/zedu", controller.ZeduLogin)
	return r, db, authService.ZeduLoginDeps{Db: db.Postgresql, ExtReq: extReq, Logger: logger}
}

func postZeduLogin(r *gin.Engine, body any) *httptest.ResponseRecorder {
	var b bytes.Buffer
	_ = json.NewEncoder(&b).Encode(body)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/zedu", &b)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func TestZeduLoginRejectsBadInput(t *testing.T) {
	r, _, _ := zeduLoginRouter(t)

	tests := []struct {
		Name         string
		Body         any
		ExpectedCode int
	}{
		{Name: "missing token", Body: map[string]string{}, ExpectedCode: http.StatusUnprocessableEntity},
		{Name: "expired zedu session", Body: models.ZeduLoginRequestModel{Token: "not-a-zedu-session"}, ExpectedCode: http.StatusUnauthorized},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			rr := postZeduLogin(r, test.Body)
			tst.AssertStatusCode(t, rr.Code, test.ExpectedCode)
		})
	}
}

func TestZeduLoginIssuesOfficeSessionForZeduUser(t *testing.T) {
	r, db, _ := zeduLoginRouter(t)
	email := strings.ToLower(fmt.Sprintf("zedu-%s@example.com", utility.RandomString(8)))
	token := zedu_mocks.ValidTokenPrefix + email

	first := postZeduLogin(r, models.ZeduLoginRequestModel{Token: token})
	tst.AssertStatusCode(t, first.Code, http.StatusOK)

	data := tst.ParseResponse(first)["data"].(map[string]any)
	if data["access_token"] == "" || data["access_token"] == nil {
		t.Fatalf("expected an office access token, got %v", data)
	}
	if _, ok := data["zedu_connect_url"]; !ok {
		t.Errorf("expected zedu_connect_url in the response so channel rooms can go live")
	}
	user := data["user"].(map[string]any)
	if user["email"] != email {
		t.Errorf("expected email %s, got %v", email, user["email"])
	}
	if user["id"] != zedu_mocks.UserID(email) {
		t.Errorf("expected the office user to reuse the zedu id %s, got %v", zedu_mocks.UserID(email), user["id"])
	}
	if user["current_org"] != zedu_mocks.OrgID(email) {
		t.Errorf("expected current org to be the zedu org %s, got %v", zedu_mocks.OrgID(email), user["current_org"])
	}

	var defaultChannels int64
	db.Postgresql.Model(&models.Channels{}).
		Where("organisation_id = ? AND name = ? AND id <> ?", zedu_mocks.OrgID(email), "general", zedu_mocks.GeneralChannelID(email)).
		Count(&defaultChannels)
	if defaultChannels != 0 {
		t.Errorf("a mirrored org should not get its own general channel, found %d", defaultChannels)
	}

	second := postZeduLogin(r, models.ZeduLoginRequestModel{Token: token})
	tst.AssertStatusCode(t, second.Code, http.StatusOK)

	var users int64
	db.Postgresql.Model(&models.User{}).Where("email = ?", email).Count(&users)
	if users != 1 {
		t.Errorf("signing in twice should reuse the account, found %d accounts", users)
	}
}

func TestSyncZeduOrgMirrorsMembersAndChannels(t *testing.T) {
	r, db, deps := zeduLoginRouter(t)
	email := strings.ToLower(fmt.Sprintf("zedu-sync-%s@example.com", utility.RandomString(8)))
	token := zedu_mocks.ValidTokenPrefix + email

	login := postZeduLogin(r, models.ZeduLoginRequestModel{Token: token})
	tst.AssertStatusCode(t, login.Code, http.StatusOK)
	// The login starts its own sync; let it finish so this one does not race it.
	authService.WaitForZeduSyncs()

	orgID := zedu_mocks.OrgID(email)
	err := authService.SyncZeduOrg(deps, authService.ZeduSyncJob{
		Token:       token,
		OrgID:       orgID,
		OwnerZeduID: zedu_mocks.UserID(email),
		CallerID:    zedu_mocks.UserID(email),
	})
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	colleague := zedu_mocks.ColleagueEmail(email)
	var colleagueUser models.User
	if err := db.Postgresql.Where("email = ?", colleague).First(&colleagueUser).Error; err != nil {
		t.Fatalf("expected the colleague to be mirrored: %v", err)
	}

	var membership models.OrgUserManagement
	if err := db.Postgresql.Where("organisation_id = ? AND user_id = ?", orgID, colleagueUser.ID).First(&membership).Error; err != nil {
		t.Fatalf("expected the colleague to be an org member: %v", err)
	}
	if membership.IsDeactivated {
		t.Errorf("expected the colleague's membership to be active")
	}

	var bots int64
	db.Postgresql.Model(&models.User{}).Where("email = ?", "bot."+email).Count(&bots)
	if bots != 0 {
		t.Errorf("zedu bots should not be mirrored as people")
	}

	var general models.Channels
	if err := db.Postgresql.Where("id = ?", zedu_mocks.GeneralChannelID(email)).First(&general).Error; err != nil {
		t.Fatalf("expected the zedu general channel under its zedu id: %v", err)
	}
	if general.OrganisationID != orgID {
		t.Errorf("expected the channel in org %s, got %s", orgID, general.OrganisationID)
	}

	var private models.Channels
	if err := db.Postgresql.Where("id = ?", zedu_mocks.PrivateChannelID(email)).First(&private).Error; err != nil {
		t.Fatalf("expected the private channel the caller is in: %v", err)
	}
	if !private.IsPrivate {
		t.Errorf("expected the private channel to stay private")
	}

	var generalMembers, privateMembers int64
	db.Postgresql.Model(&models.UserChannels{}).Where("channels_id = ?", general.ID).Count(&generalMembers)
	db.Postgresql.Model(&models.UserChannels{}).Where("channels_id = ?", private.ID).Count(&privateMembers)
	if generalMembers != 2 {
		t.Errorf("expected 2 people in general, got %d", generalMembers)
	}
	if privateMembers != 1 {
		t.Errorf("expected only the caller in the private channel, got %d", privateMembers)
	}
}

func TestZeduLoginByAMemberDoesNotMakeThemOwner(t *testing.T) {
	r, db, _ := zeduLoginRouter(t)
	owner := strings.ToLower(fmt.Sprintf("zedu-owner-%s@example.com", utility.RandomString(8)))

	login := postZeduLogin(r, models.ZeduLoginRequestModel{Token: zedu_mocks.ColleagueTokenPrefix + owner})
	tst.AssertStatusCode(t, login.Code, http.StatusOK)
	authService.WaitForZeduSyncs()

	var ownerUser, member models.User
	if err := db.Postgresql.Where("email = ?", owner).First(&ownerUser).Error; err != nil {
		t.Fatalf("expected the zedu owner to be mirrored before the member's session: %v", err)
	}
	if err := db.Postgresql.Where("email = ?", zedu_mocks.ColleagueEmail(owner)).First(&member).Error; err != nil {
		t.Fatalf("expected the member's office account: %v", err)
	}

	var org models.Organisation
	if err := db.Postgresql.Where("id = ?", zedu_mocks.OrgID(owner)).First(&org).Error; err != nil {
		t.Fatalf("expected the organisation mirror: %v", err)
	}
	if org.OwnerID != ownerUser.ID {
		t.Errorf("owner_id should be the zedu owner %s, got %s", ownerUser.ID, org.OwnerID)
	}

	var ownerRole models.OrgRole
	ownerRole, err := ownerRole.GetAOrgRoleByName(db.Postgresql, models.OrgRoleNameOwner)
	if err != nil {
		t.Fatalf("owner role: %v", err)
	}
	var membership models.OrgUserManagement
	if err := db.Postgresql.Where("organisation_id = ? AND user_id = ?", org.ID, member.ID).First(&membership).Error; err != nil {
		t.Fatalf("expected the member's membership: %v", err)
	}
	if membership.RoleID == ownerRole.ID {
		t.Errorf("the first member to sign in must not keep the Owner role")
	}
}

func TestZeduPaginationReadsTotalPages(t *testing.T) {
	// zedu-be's member and channel lists name the page count total_pages.
	body := `{"data":[],"pagination":{"current_page":1,"total_pages":3}}`

	var channels external_models.ZeduChannelsPage
	if err := json.Unmarshal([]byte(body), &channels); err != nil {
		t.Fatalf("decoding a channel page: %v", err)
	}
	if channels.Pagination.TotalPages != 3 {
		t.Errorf("expected 3 pages, got %d", channels.Pagination.TotalPages)
	}

	var members external_models.ZeduOrgMembersPage
	if err := json.Unmarshal([]byte(body), &members); err != nil {
		t.Fatalf("decoding a member page: %v", err)
	}
	if members.Pagination.TotalPages != 3 {
		t.Errorf("expected 3 pages, got %d", members.Pagination.TotalPages)
	}
}

func TestZeduLoginRefusesDeactivatedAccount(t *testing.T) {
	r, db, _ := zeduLoginRouter(t)
	email := strings.ToLower(fmt.Sprintf("zedu-deact-%s@example.com", utility.RandomString(8)))
	token := zedu_mocks.ValidTokenPrefix + email

	first := postZeduLogin(r, models.ZeduLoginRequestModel{Token: token})
	tst.AssertStatusCode(t, first.Code, http.StatusOK)
	authService.WaitForZeduSyncs()

	if err := db.Postgresql.Model(&models.User{}).Where("email = ?", email).Update("deactivated", true).Error; err != nil {
		t.Fatalf("deactivating the account: %v", err)
	}

	second := postZeduLogin(r, models.ZeduLoginRequestModel{Token: token})
	tst.AssertStatusCode(t, second.Code, http.StatusForbidden)
}
