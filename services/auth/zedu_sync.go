package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/hngprojects/telex_be/external/external_models"
	"github.com/hngprojects/telex_be/external/request"
	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/repository/storage"
)

const (
	zeduSyncInterval = 5 * time.Minute
	zeduPageLimit    = 100
	zeduMaxPages     = 50
)

// ZeduSyncJob copies one zedu organisation's members, roles, channels and
// channel members into its office mirror, reading zedu-be as CallerID would.
type ZeduSyncJob struct {
	Token       string
	OrgID       string
	OwnerZeduID string
	CallerID    string
}

// zeduMembers is the result of a member sync: the office account of every
// synced member, keyed by zedu user ID, and whether every member was read and
// prepared. Removals are only safe from a complete read.
type zeduMembers struct {
	People   map[string]officePerson
	Complete bool
}

// pendingZeduSyncs counts the background syncs still running.
var pendingZeduSyncs sync.WaitGroup

// WaitForZeduSyncs blocks until every background sync started so far has
// finished, so a test's own reads do not race the sync a login started.
func WaitForZeduSyncs() {
	pendingZeduSyncs.Wait()
}

type officePerson struct {
	ID       string
	Username string
}

type memberSpec struct {
	Org           *models.Organisation
	User          *models.User
	RoleID        string
	OverwriteRole bool
}

func startZeduSync(deps ZeduLoginDeps, job ZeduSyncJob) {
	if !claimZeduSync(job.OrgID) {
		return
	}

	pendingZeduSyncs.Add(1)
	go func() {
		defer pendingZeduSyncs.Done()
		defer func() {
			if r := recover(); r != nil {
				deps.Logger.Error("zedu sync panicked", job.OrgID, r)
			}
		}()
		if err := SyncZeduOrg(deps, job); err != nil {
			deps.Logger.Error("zedu sync failed", job.OrgID, err.Error())
		}
	}()
}

// claimZeduSync lets one sync per organisation run every zeduSyncInterval
// across all instances. Without Redis every login syncs.
func claimZeduSync(orgID string) bool {
	if storage.DB == nil || storage.DB.Redis == nil {
		return true
	}
	claimed, err := storage.DB.Redis.SetNX(context.Background(), "office:zedu-sync:"+orgID, 1, zeduSyncInterval).Result()
	return err != nil || claimed
}

func SyncZeduOrg(deps ZeduLoginDeps, job ZeduSyncJob) error {
	var org models.Organisation
	org, err := org.GetOrgByID(deps.Db, job.OrgID)
	if err != nil {
		return err
	}

	members, err := syncZeduMembers(deps, job, &org)
	if err != nil {
		return err
	}

	return syncZeduChannels(deps, job, members)
}

// holdsOfficeOwnership reports whether userID owns org in the office, by
// owner_id or by the Owner role; authorisation trusts either.
func holdsOfficeOwnership(db *gorm.DB, org models.Organisation, userID string) (bool, error) {
	if org.OwnerID == userID {
		return true, nil
	}

	var ownerRole models.OrgRole
	ownerRole, err := ownerRole.GetAOrgRoleByName(db, models.OrgRoleNameOwner)
	if err != nil {
		return false, err
	}
	var count int64
	err = db.Model(&models.OrgUserManagement{}).
		Where("organisation_id = ? AND user_id = ? AND role_id = ?", org.ID, userID, ownerRole.ID).
		Count(&count).Error
	return count > 0, err
}

// settleZeduOwnership copies roles and the owner from zedu before a session is
// issued. A mirror created by a member who is not zedu's owner starts with
// that member as owner, and authorisation trusts both the Owner role and
// owner_id, so neither may outlive the sign-in.
func settleZeduOwnership(deps ZeduLoginDeps, job ZeduSyncJob, org *models.Organisation) error {
	members, err := syncZeduMembers(deps, job, org)
	if err != nil {
		return fmt.Errorf("could not confirm the organisation's roles with zedu: %w", err)
	}
	if !members.Complete {
		return errors.New("could not confirm every member's role with zedu")
	}

	settled, err := org.GetOrgByID(deps.Db, org.ID)
	if err != nil {
		return err
	}
	if settled.OwnerID == job.CallerID {
		return errors.New("the organisation's owner could not be set up in the office")
	}
	*org = settled
	return nil
}

// syncZeduMembers mirrors the organisation's zedu members into the office.
// Members who are gone from zedu are deactivated, but only after a complete
// read, so a failed page never locks people out.
func syncZeduMembers(deps ZeduLoginDeps, job ZeduSyncJob, org *models.Organisation) (zeduMembers, error) {
	members, err := collectZeduPages(func(page int) ([]external_models.ZeduOrgMember, int, error) {
		resp, err := deps.ExtReq.SendExternalRequest(request.ZeduFetchOrgMembers, pageRequest(job.Token, job.OrgID, page))
		if err != nil {
			return nil, 0, err
		}
		result, _ := resp.(external_models.ZeduOrgMembersPage)
		return result.Data, result.Pagination.TotalPages, nil
	})
	if err != nil {
		return zeduMembers{}, fmt.Errorf("reading zedu members: %w", err)
	}

	roles := map[string]string{}
	people := map[string]officePerson{}
	complete := true

	for _, member := range members {
		if member.EntityType == "bot" || member.IsDeactivated || strings.TrimSpace(member.Email) == "" {
			continue
		}

		user, err := ensureZeduUser(deps.Db, zeduIdentity{
			ID:        member.ID,
			Email:     member.Email,
			Name:      member.Name,
			Username:  member.UserName,
			AvatarURL: member.AvatarURL,
		})
		if err != nil {
			deps.Logger.Error("zedu sync could not prepare a member", job.OrgID, err.Error())
			complete = false
			continue
		}

		spec := memberSpec{Org: org, User: &user, RoleID: roleID(deps.Db, roles, member.Role), OverwriteRole: true}
		if err := ensureMembership(deps.Db, spec); err != nil {
			deps.Logger.Error("zedu sync could not add a member", job.OrgID, err.Error())
			complete = false
			continue
		}

		people[member.ID] = officePerson{ID: user.ID, Username: user.Profile.UserName}

		if member.ID == job.OwnerZeduID && org.OwnerID != user.ID {
			if err := deps.Db.Model(&models.Organisation{}).Where("id = ?", org.ID).Update("owner_id", user.ID).Error; err != nil {
				deps.Logger.Error("zedu sync could not set the owner", job.OrgID, err.Error())
			}
		}
	}

	if complete && len(people) > 0 {
		keep := make([]string, 0, len(people))
		for _, person := range people {
			keep = append(keep, person.ID)
		}
		err := deps.Db.Model(&models.OrgUserManagement{}).
			Where("organisation_id = ? AND is_deactivated = ? AND user_id NOT IN ?", org.ID, false, keep).
			Update("is_deactivated", true).Error
		if err != nil {
			return zeduMembers{People: people, Complete: complete}, fmt.Errorf("deactivating departed members: %w", err)
		}
	}

	return zeduMembers{People: people, Complete: complete}, nil
}

// syncZeduChannels mirrors the channels the caller can see in zedu, under their
// zedu IDs, so the office web app can read and send their messages through
// zedu-be with the same room ID.
func syncZeduChannels(deps ZeduLoginDeps, job ZeduSyncJob, members zeduMembers) error {
	channels, err := collectZeduPages(func(page int) ([]external_models.ZeduChannel, int, error) {
		resp, err := deps.ExtReq.SendExternalRequest(request.ZeduFetchChannels, pageRequest(job.Token, job.OrgID, page))
		if err != nil {
			return nil, 0, err
		}
		result, _ := resp.(external_models.ZeduChannelsPage)
		return result.Data, result.Pagination.TotalPages, nil
	})
	if err != nil {
		return fmt.Errorf("reading zedu channels: %w", err)
	}

	listed := make([]string, 0, len(channels))
	complete := true
	for _, channel := range channels {
		if _, err := uuid.FromString(channel.ID); err != nil || channel.Archived {
			continue
		}

		owner := job.CallerID
		if person, ok := members.People[channel.OwnerID]; ok {
			owner = person.ID
		}
		mirror := models.Channels{
			ID:             channel.ID,
			Name:           channel.Name,
			Description:    channel.Description,
			OrganisationID: job.OrgID,
			OwnerId:        owner,
			IsPrivate:      channel.IsPrivate,
		}
		if err := upsertZeduChannel(deps.Db, mirror); err != nil {
			deps.Logger.Error("zedu sync could not mirror a channel", channel.ID, err.Error())
			complete = false
			continue
		}
		listed = append(listed, channel.ID)

		if err := members.syncChannel(deps, job, channel.ID); err != nil {
			deps.Logger.Error("zedu sync could not mirror channel members", channel.ID, err.Error())
		}
	}

	// The caller only sees the private channels they are in, so a private
	// channel missing from the list may still exist; only public ones that
	// zedu no longer lists are archived. An empty list is not taken as proof
	// that every channel is gone: it is more likely a read that went wrong.
	if !complete || len(listed) == 0 {
		return nil
	}
	return deps.Db.Model(&models.Channels{}).
		Where("organisation_id = ? AND is_private = ? AND archived = ?", job.OrgID, false, false).
		Where("id NOT IN ?", listed).
		Update("archived", true).Error
}

// upsertZeduChannel creates the office mirror of a zedu channel, or refreshes
// it; an existing mirror keeps its owner.
func upsertZeduChannel(db *gorm.DB, mirror models.Channels) error {
	var existing models.Channels
	err := db.Where("id = ?", mirror.ID).First(&existing).Error
	if err == nil {
		if existing.OrganisationID != mirror.OrganisationID {
			return fmt.Errorf("channel %s belongs to another organisation", mirror.ID)
		}
		return db.Model(&models.Channels{}).Where("id = ?", mirror.ID).Updates(map[string]any{
			"name":        mirror.Name,
			"description": mirror.Description,
			"is_private":  mirror.IsPrivate,
			"archived":    false,
		}).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return mirror.CreateChannel(db)
}

func (m zeduMembers) syncChannel(deps ZeduLoginDeps, job ZeduSyncJob, channelID string) error {
	members, err := collectZeduPages(func(page int) ([]external_models.ZeduChannelMember, int, error) {
		resp, err := deps.ExtReq.SendExternalRequest(request.ZeduFetchChanUsers, pageRequest(job.Token, channelID, page))
		if err != nil {
			return nil, 0, err
		}
		result, _ := resp.(external_models.ZeduChannelMembersPage)
		return result.Data, result.Pagination.TotalPages, nil
	})
	if err != nil {
		return err
	}

	keep := make([]string, 0, len(members))
	for _, member := range members {
		person, ok := m.People[member.ID]
		if !ok {
			continue
		}
		keep = append(keep, person.ID)

		row := models.UserChannels{ChannelsID: channelID, UserID: person.ID, Username: person.Username}
		if err := deps.Db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
	}

	// Removing anyone needs a complete member sync: a colleague who could not
	// be prepared is missing from the list without having left the channel.
	// Nobody matched also means the member sync fell short.
	if !m.Complete || len(keep) == 0 {
		return nil
	}
	return deps.Db.Where("channels_id = ? AND user_id NOT IN ?", channelID, keep).Delete(&models.UserChannels{}).Error
}

// ensureMembership adds a user to an org the way an accepted invite does, or
// restores a deactivated membership. RoleID is applied to an existing
// membership only when OverwriteRole is set.
func ensureMembership(db *gorm.DB, spec memberSpec) error {
	var existing models.OrgUserManagement
	err := db.Where("organisation_id = ? AND user_id = ?", spec.Org.ID, spec.User.ID).First(&existing).Error
	if err == nil {
		updates := map[string]any{"is_deactivated": false, "status": "active"}
		if spec.OverwriteRole && spec.RoleID != "" {
			updates["role_id"] = spec.RoleID
		}
		return db.Model(&models.OrgUserManagement{}).
			Where("organisation_id = ? AND user_id = ?", spec.Org.ID, spec.User.ID).
			Updates(updates).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	membership := models.OrgUserManagement{
		UserID:         spec.User.ID,
		OrganisationID: spec.Org.ID,
		RoleID:         spec.RoleID,
		Status:         "active",
	}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&membership).Error; err != nil {
		return err
	}

	return spec.User.AddUserToOrganisation(db, spec.User, []any{spec.Org})
}

// roleID maps a zedu role name to the office role of the same name, falling
// back to the plain member role.
func roleID(db *gorm.DB, cache map[string]string, name string) string {
	name = firstNonEmpty(name, models.OrgRoleNameUser)
	key := strings.ToLower(name)
	if id, ok := cache[key]; ok {
		return id
	}

	var role models.OrgRole
	role, err := role.GetAOrgRoleByName(db, name)
	if err != nil && !strings.EqualFold(name, models.OrgRoleNameUser) {
		return roleID(db, cache, models.OrgRoleNameUser)
	}
	cache[key] = role.ID
	return role.ID
}

func pageRequest(token, resourceID string, page int) external_models.ZeduPageRequest {
	return external_models.ZeduPageRequest{Token: token, ResourceID: resourceID, Page: page, Limit: zeduPageLimit}
}

func collectZeduPages[T any](fetch func(page int) ([]T, int, error)) ([]T, error) {
	var all []T
	for page := 1; page <= zeduMaxPages; page++ {
		items, totalPages, err := fetch(page)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		// Without a page count, reading goes on until a page comes back short:
		// stopping after page one would make a partial list look complete.
		known := totalPages > 0
		if len(items) == 0 || (known && page >= totalPages) || (!known && len(items) < zeduPageLimit) {
			return all, nil
		}
	}
	return nil, fmt.Errorf("zedu list is longer than %d pages", zeduMaxPages)
}
