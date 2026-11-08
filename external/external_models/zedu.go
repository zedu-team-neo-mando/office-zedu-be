package external_models

import (
	"encoding/json"
	"errors"
)

// ErrZeduTokenInvalid is returned when zedu-be rejects the session token.
var ErrZeduTokenInvalid = errors.New("zedu session is invalid or expired")

type ZeduOrganisation struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Email       string `json:"email"`
	Type        string `json:"type"`
	Location    string `json:"location"`
	Country     string `json:"country"`
	OwnerID     string `json:"owner_id"`
	LogoURL     string `json:"logo_url"`
}

type ZeduUser struct {
	ID           string           `json:"id"`
	Email        string           `json:"email"`
	Username     string           `json:"username"`
	Fullname     string           `json:"fullname"`
	AvatarURL    string           `json:"avatar_url"`
	CurrentOrg   string           `json:"current_org"`
	Organisation ZeduOrganisation `json:"organisation"`
}

type ZeduMeResponse struct {
	Data struct {
		User ZeduUser `json:"user"`
	} `json:"data"`
}

// UnmarshalJSON accepts both response contracts used by deployed zedu-be
// versions: {"data":{"user":{...}}} and {"data":{...}}.
func (r *ZeduMeResponse) UnmarshalJSON(payload []byte) error {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}

	var nested struct {
		User *ZeduUser `json:"user"`
	}
	if err := json.Unmarshal(envelope.Data, &nested); err != nil {
		return err
	}
	if nested.User != nil {
		r.Data.User = *nested.User
		return nil
	}

	var flat ZeduUser
	if err := json.Unmarshal(envelope.Data, &flat); err != nil {
		return err
	}
	r.Data.User = flat
	return nil
}

type ZeduOrgMember struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	UserName      string `json:"username"`
	Name          string `json:"name"`
	AvatarURL     string `json:"avatar_url"`
	Role          string `json:"role"`
	EntityType    string `json:"entity_type"`
	IsDeactivated bool   `json:"is_deactivated"`
}

type ZeduChannel struct {
	ID          string `json:"channels_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	OwnerID     string `json:"owner_id"`
	IsPrivate   bool   `json:"is_private"`
	Archived    bool   `json:"archived"`
}

type ZeduChannelMember struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// ZeduPagination is the pagination zedu-be's member and channel lists return;
// their controllers name the page count total_pages.
type ZeduPagination struct {
	CurrentPage int `json:"current_page"`
	TotalPages  int `json:"total_pages"`
}

type ZeduOrgMembersPage struct {
	Data       []ZeduOrgMember `json:"data"`
	Pagination ZeduPagination  `json:"pagination"`
}

type ZeduChannelsPage struct {
	Data       []ZeduChannel  `json:"data"`
	Pagination ZeduPagination `json:"pagination"`
}

type ZeduChannelMembersPage struct {
	Data       []ZeduChannelMember `json:"data"`
	Pagination ZeduPagination      `json:"pagination"`
}

// ZeduPageRequest asks zedu-be for one page of a list owned by ResourceID
// (an organisation for members and channels, a channel for channel members).
type ZeduPageRequest struct {
	Token      string
	ResourceID string
	Page       int
	Limit      int
}
