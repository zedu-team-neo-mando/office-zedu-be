package auth

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"
	"github.com/gosimple/slug"
	"gorm.io/gorm"

	"github.com/hngprojects/telex_be/external/external_models"
	"github.com/hngprojects/telex_be/external/request"
	"github.com/hngprojects/telex_be/external/thirdparty/zedu"
	"github.com/hngprojects/telex_be/internal/avatar"
	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/middleware"
	"github.com/hngprojects/telex_be/pkg/repository/storage/postgresql"
	"github.com/hngprojects/telex_be/services/organisation"
	"github.com/hngprojects/telex_be/utility"
	"github.com/hngprojects/telex_be/utility/audit_utility"
)

type ZeduLoginDeps struct {
	Db     *gorm.DB
	ExtReq request.ExternalRequest
	Logger *utility.Logger
}

// zeduIdentity is one zedu person, whether the caller or a synced colleague.
type zeduIdentity struct {
	ID        string
	Email     string
	Name      string
	Username  string
	AvatarURL string
}

// ZeduLogin signs a zedu user into the office without a second login: zedu-be
// confirms the session token, and the office mirrors the user and their
// current organisation before issuing its own token. Colleagues and channels
// are synced in the background.
func ZeduLogin(c *gin.Context, req models.ZeduLoginRequestModel, deps ZeduLoginDeps) (gin.H, int, error) {
	resp, err := deps.ExtReq.SendExternalRequest(request.ZeduFetchMe, req.Token)
	if err != nil {
		if errors.Is(err, external_models.ErrZeduTokenInvalid) {
			return nil, http.StatusUnauthorized, err
		}
		deps.Logger.Error("zedu session lookup failed", err.Error())
		return nil, http.StatusBadGateway, errors.New("could not confirm the zedu session")
	}

	zeduUser, _ := resp.(external_models.ZeduUser)
	zeduUser.Email = strings.ToLower(strings.TrimSpace(zeduUser.Email))
	if zeduUser.Email == "" {
		return nil, http.StatusBadGateway, errors.New("zedu returned no email for this session")
	}
	if zeduUser.Organisation.ID == "" {
		return nil, http.StatusConflict, errors.New("this zedu account has no current organisation")
	}

	user, err := ensureZeduUser(deps.Db, zeduIdentity{
		ID:        zeduUser.ID,
		Email:     zeduUser.Email,
		Name:      zeduUser.Fullname,
		Username:  zeduUser.Username,
		AvatarURL: zeduUser.AvatarURL,
	})
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("could not prepare the office account: %w", err)
	}
	if user.Deactivated {
		return nil, http.StatusForbidden, errors.New("this office account has been deactivated")
	}

	org, err := ensureZeduOrg(deps, zeduUser, req.Token, user)
	if err != nil {
		if errors.Is(err, errZeduOwnerUnconfirmed) {
			deps.Logger.Error("zedu organisation owner could not be resolved", zeduUser.Organisation.ID, err.Error())
			return nil, http.StatusServiceUnavailable, errZeduOwnerUnconfirmed
		}
		return nil, http.StatusInternalServerError, fmt.Errorf("could not prepare the office organisation: %w", err)
	}

	// zedu-be just confirmed this user belongs to the org, so a membership the
	// office had deactivated is restored. Their role is left alone here; the
	// background sync copies roles from zedu.
	var userRole models.OrgRole
	userRole, err = userRole.GetAOrgRoleByName(deps.Db, models.OrgRoleNameUser)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if err := ensureMembership(deps.Db, memberSpec{Org: &org, User: &user, RoleID: userRole.ID}); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("could not add you to the office organisation: %w", err)
	}

	// The mirror already has zedu's owner on it, so the session is issued without
	// copying roles first; the background sync keeps members and channels current.
	job := ZeduSyncJob{
		Token:       req.Token,
		OrgID:       org.ID,
		OwnerZeduID: zeduUser.Organisation.OwnerID,
		CallerID:    user.ID,
	}

	if err := deps.Db.Model(&models.User{}).Where("id = ?", user.ID).Update("current_org", org.ID).Error; err != nil {
		return nil, http.StatusInternalServerError, err
	}

	user, err = user.GetUserByEmail(deps.Db, user.Email)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}

	responseData, err := issueOfficeSession(c, deps, user)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	// Channel rooms read zedu's own conversation, so the web app subscribes to
	// zedu's realtime server.
	responseData["zedu_connect_url"] = zedu.ConnectUrl

	startZeduSync(deps, job)

	return responseData, http.StatusOK, nil
}

// ensureZeduUser finds the office account for a zedu person by email, or
// creates one. A new account reuses the zedu user ID when it is free, so the
// same person has the same ID in both apps. It has no password: it is only
// reachable through a zedu session.
func ensureZeduUser(db *gorm.DB, who zeduIdentity) (models.User, error) {
	var user models.User
	email := strings.ToLower(strings.TrimSpace(who.Email))

	if postgresql.CheckExists(db, &models.User{}, "email = ?", email) {
		return user.GetUserByEmail(db, email)
	}

	id := utility.GenerateUUID()
	if _, err := uuid.FromString(who.ID); err == nil && !postgresql.CheckExists(db, &models.User{}, "id = ?", who.ID) {
		id = who.ID
	}

	name := firstNonEmpty(who.Name, who.Username, strings.Split(email, "@")[0])
	user = models.User{
		ID:             id,
		Name:           name,
		Email:          email,
		IsVerified:     true,
		ProfileUpdated: true,
		IsOnboarded:    true,
		Profile: models.Profile{
			ID:        utility.GenerateUUID(),
			FullName:  name,
			UserName:  uniqueProfileUsername(db, firstNonEmpty(who.Username, name)),
			AvatarURL: who.AvatarURL,
		},
	}

	if err := user.CreateUser(db); err != nil {
		// Two syncs can create the same colleague at once; the loser reads the
		// winner's row instead of failing.
		if postgresql.CheckExists(db, &models.User{}, "email = ?", email) {
			return user.GetUserByEmail(db, email)
		}
		return user, err
	}

	return user.GetUserByEmail(db, email)
}

func uniqueProfileUsername(db *gorm.DB, base string) string {
	username := strings.ToLower(strings.TrimSpace(base))
	for i := 0; i < 5; i++ {
		if !postgresql.CheckExists(db, &models.Profile{}, "user_name = ?", username) {
			return username
		}
		username = fmt.Sprintf("%s_%s", strings.ToLower(strings.TrimSpace(base)), utility.GenerateUUID()[:8])
	}
	return username
}

// ensureZeduOrg returns the office mirror of a zedu organisation, creating it
// under the zedu org's own ID. Its channels come from the zedu sync, so no
// default "general" channel is made.
//
// The owner is resolved before anything is written. The colleague who signs in
// first is often not zedu's owner, and authorisation trusts owner_id as much as
// the Owner role, so a mirror that starts with the wrong owner hands that member
// privileges they do not have in zedu. Refusing the sign-in leaves nothing
// behind; creating the mirror and correcting it afterwards leaves a window in
// which the wrong owner can switch into it.
func ensureZeduOrg(deps ZeduLoginDeps, zeduUser external_models.ZeduUser, token string, caller models.User) (models.Organisation, error) {
	var org models.Organisation
	zorg := zeduUser.Organisation

	if postgresql.CheckExists(deps.Db, &models.Organisation{}, "id = ?", zorg.ID) {
		return org.GetOrgByID(deps.Db, zorg.ID)
	}
	if _, err := uuid.FromString(zorg.ID); err != nil {
		return org, fmt.Errorf("zedu organisation id %q is not a uuid", zorg.ID)
	}

	ownerID := caller.ID
	creatorRole := models.OrgRoleNameOwner
	if zorg.OwnerID != "" && zorg.OwnerID != zeduUser.ID {
		owner, err := zeduOwnerAccount(deps, zorg, token)
		if err != nil {
			return org, err
		}
		ownerID = owner.ID
		creatorRole = models.OrgRoleNameUser
	}

	createReq := models.CreateOrgRequestModel{
		ID:                 zorg.ID,
		SkipDefaultChannel: true,
		OwnerUserID:        ownerID,
		CreatorRoleName:    creatorRole,
		Name:               firstNonEmpty(zorg.Name, "Zedu organisation"),
		Description:        zorg.Description,
		Email:              zorg.Email,
		Type:               firstNonEmpty(zorg.Type, "team"),
		Location:           zorg.Location,
		Country:            zorg.Country,
	}

	err := deps.Db.Transaction(func(tx *gorm.DB) error {
		if _, err := organisation.CreateOrganisation(createReq, tx, caller.ID, deps.Logger); err != nil {
			return err
		}
		// The logo is a zedu-hosted URL; store it as is rather than re-upload it.
		if zorg.LogoURL == "" {
			return nil
		}
		return tx.Model(&models.Organisation{}).Where("id = ?", zorg.ID).Update("logo_url", zorg.LogoURL).Error
	})
	if err != nil {
		// Two colleagues opening the office at once both try to create the
		// mirror; the loser uses the winner's.
		if postgresql.CheckExists(deps.Db, &models.Organisation{}, "id = ?", zorg.ID) {
			return org.GetOrgByID(deps.Db, zorg.ID)
		}
		return org, err
	}

	return org.GetOrgByID(deps.Db, zorg.ID)
}

// errZeduOwnerUnconfirmed reports that zedu's owner could not be resolved, so
// the mirror must not be created at all.
var errZeduOwnerUnconfirmed = errors.New("could not confirm the organisation's owner with zedu")

// zeduOwnerAccount resolves zedu's owner to their office account, creating it
// when they have never signed in. The member list is read in full first: a
// failed or empty read is not evidence of who owns the organisation, so the
// caller is refused rather than made the owner by default.
func zeduOwnerAccount(deps ZeduLoginDeps, zorg external_models.ZeduOrganisation, token string) (models.User, error) {
	members, err := readZeduMembers(deps, token, zorg.ID)
	if err != nil {
		return models.User{}, fmt.Errorf("%w: %v", errZeduOwnerUnconfirmed, err)
	}

	for _, member := range members {
		if member.ID != zorg.OwnerID {
			continue
		}
		if member.EntityType == "bot" || member.IsDeactivated || strings.TrimSpace(member.Email) == "" {
			break
		}

		owner, err := ensureZeduUser(deps.Db, zeduIdentity{
			ID:        member.ID,
			Email:     member.Email,
			Name:      member.Name,
			Username:  member.UserName,
			AvatarURL: member.AvatarURL,
		})
		if err != nil {
			return models.User{}, fmt.Errorf("%w: %v", errZeduOwnerUnconfirmed, err)
		}
		return owner, nil
	}

	return models.User{}, fmt.Errorf("%w: the member list does not include the owner", errZeduOwnerUnconfirmed)
}

func issueOfficeSession(c *gin.Context, deps ZeduLoginDeps, user models.User) (gin.H, error) {
	tokenData, err := middleware.CreateToken(user, c)
	if err != nil {
		return nil, fmt.Errorf("error creating token: %v", err.Error())
	}

	tokens := map[string]string{
		"access_token": tokenData.AccessToken,
		"exp":          strconv.Itoa(int(tokenData.ExpiresAt.Unix())),
	}

	accessToken := models.AccessToken{ID: tokenData.AccessUuid, OwnerID: user.ID}
	if err := accessToken.CreateAccessToken(deps.Db, tokens); err != nil {
		return nil, fmt.Errorf("error saving token: %v", err.Error())
	}

	var org models.Organisation
	org, _ = org.GetOrgByID(deps.Db, user.CurrentOrg.String())

	audit_utility.LogUserLogin(c, deps.Db, deps.ExtReq, user.ID, tokenData.AccessUuid, user.Organisations)

	return gin.H{
		"user": map[string]any{
			"id":                        user.ID,
			"user_id":                   user.ID,
			"email":                     user.Email,
			"username":                  user.Name,
			"fullname":                  user.Name,
			"current_org":               user.CurrentOrg,
			"current_organisation_slug": slug.Make(org.Name),
			"is_verified":               user.IsVerified,
			"profile_updated":           user.ProfileUpdated,
			"is_onboarded":              user.IsOnboarded,
			"avatar_url":                user.Profile.AvatarURL,
			"default_avatar_url":        avatar.GenerateDefaultAvatarURL(user.ID),
			"expires_in":                strconv.Itoa(int(tokenData.ExpiresAt.Unix())),
			"created_at":                strconv.Itoa(int(user.CreatedAt.Unix())),
			"updated_at":                strconv.Itoa(int(user.UpdatedAt.Unix())),
			"organisation":              org,
			"online":                    user.Profile.Online,
		},
		"access_token":            tokenData.AccessToken,
		"access_token_expires_in": strconv.Itoa(int(tokenData.ExpiresAt.Unix())),
		"notification_token":      accessToken.SubAccessToken,
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
