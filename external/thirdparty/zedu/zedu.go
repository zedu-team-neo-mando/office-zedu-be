package zedu

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hngprojects/telex_be/external/external_models"
)

const maxResponseBytes = 8 << 20

// ApiUrl is the main zedu-be the office trusts to confirm a desktop user's
// session and to read their organisation, channels and members from. It must
// be the zedu-be Zedu Desktop signs in to.
//
// TODO: migrate to a ZEDU_API_URL setting in internal/config once new env
// values can be added.
const ApiUrl = "https://api.zedu.chat/api/v1"

// ConnectUrl is zedu-be's Centrifugo websocket, handed to the office web app
// at sign-in so channel rooms and DMs receive zedu messages live. Empty would
// make the web app re-read zedu every few seconds instead.
//
// TODO: migrate to a ZEDU_CONNECT_URL setting in internal/config once new env
// values can be added.
const ConnectUrl = "wss://api.zedu.chat/centrifugo/connection/websocket"

var httpClient = &http.Client{Timeout: 15 * time.Second}

// Client reads from zedu-be on behalf of a signed-in zedu user. It does not
// go through external.SendRequest, because that logs request headers and
// every call here carries the user's zedu session token.
type Client struct {
	BaseUrl string
}

// FetchMe returns the user a zedu session belongs to, with their current
// organisation. zedu-be has no GET /users/me of its own: that path is answered
// by GET /users/:user_id with "me" as the ID, which returns the user with an
// empty email and ID. The user is read from GET /profile instead, as Zedu
// Desktop does, and the organisation from GET /organisations/:id.
func (c Client) FetchMe(token string) (external_models.ZeduUser, error) {
	var profile external_models.ZeduProfileResponse
	if err := c.get("/profile", token, &profile); err != nil {
		return external_models.ZeduUser{}, err
	}

	user := external_models.ZeduUser{
		ID:         profile.Data.UserID,
		Email:      profile.Data.Email,
		Username:   profile.Data.Username,
		Fullname:   profile.Data.FullName,
		AvatarURL:  profile.Data.AvatarURL,
		CurrentOrg: profile.Data.OrganisationID,
	}
	if user.CurrentOrg == "" {
		return user, nil
	}

	var org external_models.ZeduOrganisationResponse
	if err := c.get("/organisations/"+url.PathEscape(user.CurrentOrg), token, &org); err != nil {
		return external_models.ZeduUser{}, err
	}
	user.Organisation = org.Data
	return user, nil
}

func (c Client) FetchOrgMembers(req external_models.ZeduPageRequest) (external_models.ZeduOrgMembersPage, error) {
	var resp external_models.ZeduOrgMembersPage
	path := fmt.Sprintf("/organisations/%s/users?%s", url.PathEscape(req.ResourceID), pageQuery(req))
	err := c.get(path, req.Token, &resp)
	return resp, err
}

func (c Client) FetchOrgChannels(req external_models.ZeduPageRequest) (external_models.ZeduChannelsPage, error) {
	var resp external_models.ZeduChannelsPage
	path := fmt.Sprintf("/organisations/%s/channels?%s", url.PathEscape(req.ResourceID), pageQuery(req))
	err := c.get(path, req.Token, &resp)
	return resp, err
}

func (c Client) FetchChannelMembers(req external_models.ZeduPageRequest) (external_models.ZeduChannelMembersPage, error) {
	var resp external_models.ZeduChannelMembersPage
	path := fmt.Sprintf("/channels/%s/users?%s", url.PathEscape(req.ResourceID), pageQuery(req))
	err := c.get(path, req.Token, &resp)
	return resp, err
}

func pageQuery(req external_models.ZeduPageRequest) string {
	values := url.Values{}
	values.Set("page", fmt.Sprint(req.Page))
	values.Set("limit", fmt.Sprint(req.Limit))
	return values.Encode()
}

func (c Client) get(path, token string, out any) error {
	if c.BaseUrl == "" {
		return errors.New("the zedu API address is not set")
	}
	if token == "" {
		return external_models.ErrZeduTokenInvalid
	}

	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(c.BaseUrl, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	res, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("zedu request failed: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusUnauthorized {
		return external_models.ErrZeduTokenInvalid
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("zedu answered %d for %s", res.StatusCode, strings.SplitN(path, "?", 2)[0])
	}

	return json.NewDecoder(io.LimitReader(res.Body, maxResponseBytes)).Decode(out)
}
