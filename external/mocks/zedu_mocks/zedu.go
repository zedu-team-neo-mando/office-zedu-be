package zedu_mocks

import (
	"strings"

	"github.com/gofrs/uuid"

	"github.com/hngprojects/telex_be/external/external_models"
)

// ValidTokenPrefix marks a mock zedu session: "zedu-valid:<email>" signs in as
// <email>. Anything else is rejected as an expired session.
const ValidTokenPrefix = "zedu-valid:"

// ColleagueTokenPrefix marks a mock session of the colleague in <email>'s
// organisation: "zedu-colleague:<email>" signs in as ColleagueEmail(email),
// a member who is not the owner.
const ColleagueTokenPrefix = "zedu-colleague:"

// emailFromToken returns the owner email the mock organisation is keyed by.
func emailFromToken(token string) (string, bool) {
	for _, prefix := range []string{ValidTokenPrefix, ColleagueTokenPrefix} {
		if email, ok := strings.CutPrefix(token, prefix); ok && email != "" {
			return strings.ToLower(email), true
		}
	}
	return "", false
}

func stableID(kind, key string) string {
	return uuid.NewV5(uuid.NamespaceURL, "zedu-mock:"+kind+":"+key).String()
}

func UserID(email string) string         { return stableID("user", strings.ToLower(email)) }
func OrgID(email string) string          { return stableID("org", strings.ToLower(email)) }
func ColleagueEmail(email string) string { return "colleague." + strings.ToLower(email) }
func GeneralChannelID(email string) string {
	return stableID("channel-general", strings.ToLower(email))
}
func PrivateChannelID(email string) string {
	return stableID("channel-private", strings.ToLower(email))
}

func FetchMe(data any) (external_models.ZeduUser, error) {
	token, _ := data.(string)
	email, ok := emailFromToken(token)
	if !ok {
		return external_models.ZeduUser{}, external_models.ErrZeduTokenInvalid
	}

	who := email
	if strings.HasPrefix(token, ColleagueTokenPrefix) {
		who = ColleagueEmail(email)
	}

	return external_models.ZeduUser{
		ID:         UserID(who),
		Email:      who,
		Username:   "mock_" + strings.Split(who, "@")[0],
		Fullname:   "Mock User",
		CurrentOrg: OrgID(email),
		Organisation: external_models.ZeduOrganisation{
			ID:      OrgID(email),
			Name:    "Mock Org " + email,
			Email:   email,
			Type:    "team",
			Country: "nigeria",
			OwnerID: UserID(email),
		},
	}, nil
}

func FetchOrgMembers(data any) (external_models.ZeduOrgMembersPage, error) {
	req, _ := data.(external_models.ZeduPageRequest)
	email, ok := emailFromToken(req.Token)
	if !ok {
		return external_models.ZeduOrgMembersPage{}, external_models.ErrZeduTokenInvalid
	}

	colleague := ColleagueEmail(email)
	return external_models.ZeduOrgMembersPage{
		Data: []external_models.ZeduOrgMember{
			{ID: UserID(email), Email: email, UserName: "mock_owner", Name: "Mock Owner", Role: "Owner", EntityType: "user"},
			{ID: UserID(colleague), Email: colleague, UserName: "mock_colleague", Name: "Mock Colleague", Role: "User", EntityType: "user"},
			{ID: stableID("bot", email), Email: "bot." + email, Name: "Mock Bot", Role: "Bot", EntityType: "bot"},
		},
		Pagination: external_models.ZeduPagination{CurrentPage: 1, TotalPagesCount: 1},
	}, nil
}

func FetchOrgChannels(data any) (external_models.ZeduChannelsPage, error) {
	req, _ := data.(external_models.ZeduPageRequest)
	email, ok := emailFromToken(req.Token)
	if !ok {
		return external_models.ZeduChannelsPage{}, external_models.ErrZeduTokenInvalid
	}

	return external_models.ZeduChannelsPage{
		Data: []external_models.ZeduChannel{
			{ID: GeneralChannelID(email), Name: "general", Description: "everyone", OwnerID: UserID(email)},
			{ID: PrivateChannelID(email), Name: "leads", Description: "private", OwnerID: UserID(email), IsPrivate: true},
		},
		Pagination: external_models.ZeduPagination{CurrentPage: 1, TotalPagesCount: 1},
	}, nil
}

func FetchChannelMembers(data any) (external_models.ZeduChannelMembersPage, error) {
	req, _ := data.(external_models.ZeduPageRequest)
	email, ok := emailFromToken(req.Token)
	if !ok {
		return external_models.ZeduChannelMembersPage{}, external_models.ErrZeduTokenInvalid
	}

	members := []external_models.ZeduChannelMember{{ID: UserID(email), Email: email}}
	if req.ResourceID == GeneralChannelID(email) {
		colleague := ColleagueEmail(email)
		members = append(members, external_models.ZeduChannelMember{ID: UserID(colleague), Email: colleague})
	}

	return external_models.ZeduChannelMembersPage{
		Data:       members,
		Pagination: external_models.ZeduPagination{CurrentPage: 1, TotalPagesCount: 1},
	}, nil
}
