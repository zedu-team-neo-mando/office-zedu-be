package office

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"

	"github.com/hngprojects/telex_be/external/request"
	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/pkg/middleware"
	"github.com/hngprojects/telex_be/pkg/repository/storage"
	service "github.com/hngprojects/telex_be/services/office"
	"github.com/hngprojects/telex_be/utility"
)

// Controller exposes the office API.
type Controller struct {
	Db        *storage.Database
	Validator *validator.Validate
	Logger    *utility.Logger
	ExtReq    request.ExternalRequest
}

// identity is the requester behind a call.
type identity struct {
	UserID string
	OrgID  string
}

// identityOf pulls the authenticated user and organisation out of the token
// claims set by the Authorize middleware.
func (base *Controller) identityOf(c *gin.Context) (identity, error) {
	userID, err := middleware.GetUserClaims(c, base.Db.Postgresql, "user_id")
	if err != nil {
		return identity{}, err
	}

	orgID, err := middleware.GetUserClaims(c, base.Db.Postgresql, "org_id")
	if err != nil {
		return identity{}, err
	}

	user, ok := userID.(string)
	if !ok || user == "" {
		return identity{}, errors.New("unable to read the user id from the token")
	}

	org, ok := orgID.(string)
	if !ok || org == "" {
		return identity{}, errors.New("unable to read the organisation id from the token")
	}

	return identity{UserID: user, OrgID: org}, nil
}

func (base *Controller) service() *service.Service {
	return service.New(base.Db, base.Logger)
}

// fail logs the real error and returns a 500 JSON response. Because
// log.json has console.enable: false, errors land in a file inside the
// container and Coolify shows nothing, so we also mirror to stdout.
func (base *Controller) fail(c *gin.Context, message string, err error) {
	base.Logger.Error("%s: %v", message, err)
	fmt.Printf("ERROR: %s: %v\n", message, err)
	rd := utility.BuildErrorResponse(http.StatusInternalServerError, "error", message, err, nil)
	c.JSON(http.StatusInternalServerError, rd)
}

// authorise resolves the requester and answers 403 when the organisation has
// The Office switched off. Every office route except the status goes through
// it, so a disabled organisation keeps no door open.
func (base *Controller) authorise(c *gin.Context) (identity, bool) {
	requester, err := base.identityOf(c)
	if err != nil {
		rd := utility.BuildErrorResponse(http.StatusUnauthorized, "error", "unable to get user claims", err, nil)
		c.JSON(http.StatusUnauthorized, rd)
		return identity{}, false
	}

	enabled, err := base.service().IsEnabled(requester.OrgID)
	if err != nil {
		base.fail(c, "failed to read the office settings", err)
		return identity{}, false
	}

	if !enabled {
		rd := utility.BuildErrorResponse(http.StatusForbidden, "office_disabled", "the office is switched off for this organisation", nil, nil)
		c.JSON(http.StatusForbidden, rd)
		return identity{}, false
	}

	return requester, true
}

// Status reports whether the organisation has The Office switched on. It is
// the one office route that answers for a disabled organisation, so the
// client can ask before it offers the office.
func (base *Controller) Status(c *gin.Context) {
	requester, err := base.identityOf(c)
	if err != nil {
		rd := utility.BuildErrorResponse(http.StatusUnauthorized, "error", "unable to get user claims", err, nil)
		c.JSON(http.StatusUnauthorized, rd)
		return
	}

	enabled, err := base.service().IsEnabled(requester.OrgID)
	if err != nil {
		base.fail(c, "failed to read the office settings", err)
		return
	}

	rd := utility.BuildSuccessResponse(http.StatusOK, "office status", models.OfficeStatusResponse{Enabled: enabled})
	c.JSON(http.StatusOK, rd)
}

// GetSettings returns the organisation's office options. It answers even when
// the office is switched off, so the setting can be turned back on.
func (base *Controller) GetSettings(c *gin.Context) {
	requester, err := base.identityOf(c)
	if err != nil {
		rd := utility.BuildErrorResponse(http.StatusUnauthorized, "error", "unable to get user claims", err, nil)
		c.JSON(http.StatusUnauthorized, rd)
		return
	}

	settings, err := base.service().Settings(requester.OrgID)
	if err != nil {
		base.fail(c, "failed to load the office settings", err)
		return
	}

	rd := utility.BuildSuccessResponse(http.StatusOK, "office settings", settings)
	c.JSON(http.StatusOK, rd)
}

// UpdateSettings turns the office on or off for the organisation. Only the
// organisation's owners and administrators may do it.
func (base *Controller) UpdateSettings(c *gin.Context) {
	requester, err := base.identityOf(c)
	if err != nil {
		rd := utility.BuildErrorResponse(http.StatusUnauthorized, "error", "unable to get user claims", err, nil)
		c.JSON(http.StatusUnauthorized, rd)
		return
	}

	var request models.OfficeSettingsRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "failed to parse request body", err, nil)
		c.JSON(http.StatusBadRequest, rd)
		return
	}

	if err := base.Validator.Struct(&request); err != nil {
		rd := utility.BuildErrorResponse(http.StatusUnprocessableEntity, "error", "validation failed", utility.ValidationResponse(err, base.Validator), nil)
		c.JSON(http.StatusUnprocessableEntity, rd)
		return
	}

	office := base.service()
	if !office.IsOrganisationAdmin(requester.OrgID, requester.UserID) {
		rd := utility.BuildErrorResponse(http.StatusForbidden, "forbidden", "only organisation administrators can change the office settings", nil, nil)
		c.JSON(http.StatusForbidden, rd)
		return
	}

	settings, err := office.Settings(requester.OrgID)
	if err != nil {
		base.fail(c, "failed to load the office settings", err)
		return
	}

	settings.Enabled = *request.Enabled

	if err := office.UpdateSettings(requester.OrgID, settings); err != nil {
		base.fail(c, "failed to save the office settings", err)
		return
	}

	base.Logger.Info("office settings updated for organisation %s", requester.OrgID)
	rd := utility.BuildSuccessResponse(http.StatusOK, "office settings updated", settings)
	c.JSON(http.StatusOK, rd)
}

// Bootstrap returns the building, its settings, the room directory and the
// published layouts the requester may see.
func (base *Controller) Bootstrap(c *gin.Context) {
	requester, ok := base.authorise(c)
	if !ok {
		return
	}

	office := base.service()

	// Fetch access sets once (3 queries) instead of per-member CanAccess calls
	sets, err := office.AccessSets(requester.OrgID, requester.UserID)
	if err != nil {
		base.fail(c, "failed to load access sets", err)
		return
	}

	directory, err := office.BuildDirectory(requester.OrgID, requester.UserID, sets)
	if err != nil {
		base.fail(c, "failed to build the office directory", err)
		return
	}

	org := models.Organisation{}
	organisation, err := org.GetOrgByID(base.Db.Postgresql, requester.OrgID)
	if err != nil {
		base.fail(c, "failed to load the organisation", err)
		return
	}

	settings := organisation.OfficeSettings
	enabled, err := office.IsEnabled(requester.OrgID)
	if err != nil {
		base.fail(c, "failed to read the office settings", err)
		return
	}
	settings.Enabled = enabled

	// Batch fetch all published layouts in one query instead of per-room
	layouts := map[string]models.OfficeLayoutResponse{}
	publishedLayouts, err := office.GetAllPublishedLayouts(requester.OrgID)
	if err != nil {
		base.fail(c, "failed to load layouts", err)
		return
	}
	for _, layout := range publishedLayouts {
		roomID := service.EncodeRoomID(layout.RoomKind, layout.RefID)
		layouts[roomID] = service.ToResponse(layout)
	}

	// Personal layout for the requester (if they have one)
	personalLayout, err := office.GetPersonalLayout(requester.OrgID, requester.UserID)
	if err == nil && personalLayout != nil {
		roomID := service.EncodeRoomID(models.OfficeRoomKindPersonal, requester.UserID)
		layouts[roomID] = service.ToResponse(personalLayout)
	}

	payload := models.OfficeBootstrapResponse{
		Building: models.OfficeBuildingResponse{
			ID:    requester.OrgID,
			OrgID: requester.OrgID,
			Name:  organisation.Name,
		},
		Settings:       settings,
		Floors:         service.FloorDirectory(directory.Floors),
		Members:        directory.Members,
		Rooms:          directory.Rooms,
		Layouts:        layouts,
		ChannelRoomMap: directory.ChannelRoomMap,
		DMOfficeMap:    directory.DMOfficeMap,
	}

	rd := utility.BuildSuccessResponse(http.StatusOK, "office bootstrap", payload)
	c.JSON(http.StatusOK, rd)
}

// GetRoom returns the metadata of a single room plus the access result.
func (base *Controller) GetRoom(c *gin.Context) {
	requester, ok := base.authorise(c)
	if !ok {
		return
	}

	office := base.service()

	ref, err := service.ParseRoomID(c.Param("roomId"))
	if err != nil {
		rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "invalid room id", err, nil)
		c.JSON(http.StatusBadRequest, rd)
		return
	}

	access, err := office.CanAccess(requester.OrgID, requester.UserID, ref)
	if err != nil {
		base.fail(c, "failed to check room access", err)
		return
	}
	if !access.CanEnter {
		rd := utility.BuildErrorResponse(http.StatusForbidden, "error", "you cannot enter this room", nil, nil)
		c.JSON(http.StatusForbidden, rd)
		return
	}

	// Fetch access sets for the room lookup
	sets, err := office.AccessSets(requester.OrgID, requester.UserID)
	if err != nil {
		base.fail(c, "failed to load access sets", err)
		return
	}

	room, err := office.GetRoom(requester.OrgID, requester.UserID, sets, ref)
	if err != nil {
		base.fail(c, "failed to load the room", err)
		return
	}
	if room == nil {
		rd := utility.BuildErrorResponse(http.StatusNotFound, "error", "room not found", nil, nil)
		c.JSON(http.StatusNotFound, rd)
		return
	}

	rd := utility.BuildSuccessResponse(http.StatusOK, "room", map[string]any{"room": room, "access": access})
	c.JSON(http.StatusOK, rd)
}

// GetLayout returns the live layout of a room.
func (base *Controller) GetLayout(c *gin.Context) {
	requester, ok := base.authorise(c)
	if !ok {
		return
	}

	office := base.service()

	ref, err := service.ParseRoomID(c.Param("roomId"))
	if err != nil {
		rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "invalid room id", err, nil)
		c.JSON(http.StatusBadRequest, rd)
		return
	}

	access, err := office.CanAccess(requester.OrgID, requester.UserID, ref)
	if err != nil {
		base.fail(c, "failed to check room access", err)
		return
	}
	if !access.CanEnter {
		rd := utility.BuildErrorResponse(http.StatusForbidden, "error", "you cannot view this room", nil, nil)
		c.JSON(http.StatusForbidden, rd)
		return
	}

	layout, err := office.GetPublishedLayout(requester.OrgID, ref)
	if err != nil {
		base.fail(c, "failed to load the room layout", err)
		return
	}

	if layout == nil && ref.Kind == models.OfficeRoomKindPersonal && ref.Ref == requester.UserID {
		layout, err = office.GetPersonalLayout(requester.OrgID, requester.UserID)
		if err != nil {
			base.fail(c, "failed to load the room layout", err)
			return
		}
	}

	if layout == nil {
		rd := utility.BuildSuccessResponse(http.StatusOK, "room layout", models.OfficeLayoutResponse{
			RoomID:  service.EncodeRoomID(ref.Kind, ref.Ref),
			Version: 0,
			Status:  "",
			Grid:    models.OfficeGrid{Width: int(service.OfficeBuildingWidth), Height: int(service.OfficeBuildingHeight), Tile: service.OfficeTileSize},
			Items:   []models.OfficeFurnitureItem{},
		})
		c.JSON(http.StatusOK, rd)
		return
	}

	rd := utility.BuildSuccessResponse(http.StatusOK, "room layout", service.ToResponse(layout))
	c.JSON(http.StatusOK, rd)
}

// GetDraft returns the admin's working copy of a shared room layout.
func (base *Controller) GetDraft(c *gin.Context) {
	requester, ok := base.authorise(c)
	if !ok {
		return
	}

	office := base.service()

	ref, err := service.ParseRoomID(c.Param("roomId"))
	if err != nil {
		rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "invalid room id", err, nil)
		c.JSON(http.StatusBadRequest, rd)
		return
	}

	access, err := office.CanAccess(requester.OrgID, requester.UserID, ref)
	if err != nil {
		base.fail(c, "failed to check room access", err)
		return
	}
	if !access.CanEditFurniture {
		rd := utility.BuildErrorResponse(http.StatusForbidden, "error", "only administrators can edit shared furniture", nil, nil)
		c.JSON(http.StatusForbidden, rd)
		return
	}

	draft, err := office.GetDraft(requester.OrgID, ref, requester.UserID)
	if err != nil {
		base.fail(c, "failed to load the room draft", err)
		return
	}

	if draft == nil {
		rd := utility.BuildSuccessResponse(http.StatusOK, "room draft", models.OfficeLayoutResponse{
			RoomID: service.EncodeRoomID(ref.Kind, ref.Ref),
			Items:  []models.OfficeFurnitureItem{},
		})
		c.JSON(http.StatusOK, rd)
		return
	}

	rd := utility.BuildSuccessResponse(http.StatusOK, "room draft", service.ToResponse(draft))
	c.JSON(http.StatusOK, rd)
}

// SaveDraft stores the admin's working copy of a shared room layout.
func (base *Controller) SaveDraft(c *gin.Context) {
	requester, ok := base.authorise(c)
	if !ok {
		return
	}

	office := base.service()

	ref, err := service.ParseRoomID(c.Param("roomId"))
	if err != nil {
		rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "invalid room id", err, nil)
		c.JSON(http.StatusBadRequest, rd)
		return
	}

	access, err := office.CanAccess(requester.OrgID, requester.UserID, ref)
	if err != nil {
		base.fail(c, "failed to check room access", err)
		return
	}
	if !access.CanEditFurniture {
		rd := utility.BuildErrorResponse(http.StatusForbidden, "error", "only administrators can edit shared furniture", nil, nil)
		c.JSON(http.StatusForbidden, rd)
		return
	}

	var request models.OfficeLayoutRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "invalid request body", err, nil)
		c.JSON(http.StatusBadRequest, rd)
		return
	}

	draft, err := office.SaveDraft(models.OfficeLayoutCommand{
		OrganisationID: requester.OrgID,
		Room:           ref,
		ActorID:        requester.UserID,
		Payload:        models.OfficeLayoutPayload{Grid: request.Grid, Items: request.Items},
	})
	if err != nil {
		if errors.Is(err, service.ErrInvalidLayout) {
			rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "invalid layout", err, nil)
			c.JSON(http.StatusBadRequest, rd)
			return
		}
		base.fail(c, "failed to save the room draft", err)
		return
	}

	rd := utility.BuildSuccessResponse(http.StatusOK, "room draft saved", service.ToResponse(draft))
	c.JSON(http.StatusOK, rd)
}

// PublishLayout publishes the admin's draft as the live layout of a room.
func (base *Controller) PublishLayout(c *gin.Context) {
	requester, ok := base.authorise(c)
	if !ok {
		return
	}

	office := base.service()

	ref, err := service.ParseRoomID(c.Param("roomId"))
	if err != nil {
		rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "invalid room id", err, nil)
		c.JSON(http.StatusBadRequest, rd)
		return
	}

	access, err := office.CanAccess(requester.OrgID, requester.UserID, ref)
	if err != nil {
		base.fail(c, "failed to check room access", err)
		return
	}
	if !access.CanPublish {
		rd := utility.BuildErrorResponse(http.StatusForbidden, "error", "only administrators can publish shared layouts", nil, nil)
		c.JSON(http.StatusForbidden, rd)
		return
	}

	var request models.OfficePublishLayoutRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&request); err != nil {
			rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "invalid request body", err, nil)
			c.JSON(http.StatusBadRequest, rd)
			return
		}
	}

	layout, err := office.Publish(models.OfficeLayoutCommand{
		OrganisationID: requester.OrgID,
		Room:           ref,
		ActorID:        requester.UserID,
		Version:        request.Version,
	})
	if err != nil {
		if errors.Is(err, service.ErrVersionConflict) {
			rd := utility.BuildErrorResponse(http.StatusConflict, "error", "the layout was changed by someone else", err, nil)
			c.JSON(http.StatusConflict, rd)
			return
		}
		base.fail(c, "failed to publish the layout", err)
		return
	}

	office.PublishLayoutUpdate(service.NewChannelTarget(requester.OrgID, ref), layout.Version, requester.UserID)

	rd := utility.BuildSuccessResponse(http.StatusOK, "layout published", service.ToResponse(layout))
	c.JSON(http.StatusOK, rd)
}

// ResetLayout restores a shared room to its template.
func (base *Controller) ResetLayout(c *gin.Context) {
	requester, ok := base.authorise(c)
	if !ok {
		return
	}

	office := base.service()

	ref, err := service.ParseRoomID(c.Param("roomId"))
	if err != nil {
		rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "invalid room id", err, nil)
		c.JSON(http.StatusBadRequest, rd)
		return
	}

	access, err := office.CanAccess(requester.OrgID, requester.UserID, ref)
	if err != nil {
		base.fail(c, "failed to check room access", err)
		return
	}
	if !access.CanReset {
		rd := utility.BuildErrorResponse(http.StatusForbidden, "error", "only administrators can reset a room", nil, nil)
		c.JSON(http.StatusForbidden, rd)
		return
	}

	layout, err := office.Reset(requester.OrgID, ref, requester.UserID)
	if err != nil {
		base.fail(c, "failed to reset the room", err)
		return
	}

	office.PublishLayoutUpdate(service.NewChannelTarget(requester.OrgID, ref), layout.Version, requester.UserID)

	rd := utility.BuildSuccessResponse(http.StatusOK, "room reset", service.ToResponse(layout))
	c.JSON(http.StatusOK, rd)
}

// SavePersonalLayout autosaves the layout of the requester's own office.
func (base *Controller) SavePersonalLayout(c *gin.Context) {
	requester, ok := base.authorise(c)
	if !ok {
		return
	}

	office := base.service()

	ref, err := service.ParseRoomID(c.Param("roomId"))
	if err != nil {
		rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "invalid room id", err, nil)
		c.JSON(http.StatusBadRequest, rd)
		return
	}

	if ref.Kind != models.OfficeRoomKindPersonal {
		rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "this route only saves personal offices", nil, nil)
		c.JSON(http.StatusBadRequest, rd)
		return
	}

	if ref.Ref != requester.UserID {
		rd := utility.BuildErrorResponse(http.StatusForbidden, "error", "you can only edit your own office", nil, nil)
		c.JSON(http.StatusForbidden, rd)
		return
	}

	var request models.OfficeLayoutRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "invalid request body", err, nil)
		c.JSON(http.StatusBadRequest, rd)
		return
	}

	layout, err := office.SavePersonalLayout(requester.OrgID, requester.UserID, models.OfficeLayoutPayload{
		Grid:  request.Grid,
		Items: request.Items,
	})
	if err != nil {
		if errors.Is(err, service.ErrInvalidLayout) {
			rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "invalid layout", err, nil)
			c.JSON(http.StatusBadRequest, rd)
			return
		}
		base.fail(c, "failed to save your office", err)
		return
	}

	rd := utility.BuildSuccessResponse(http.StatusOK, "office saved", service.ToResponse(layout))
	c.JSON(http.StatusOK, rd)
}

// Occupants returns the people currently standing in a room.
func (base *Controller) Occupants(c *gin.Context) {
	requester, ok := base.authorise(c)
	if !ok {
		return
	}

	office := base.service()

	ref, err := service.ParseRoomID(c.Param("roomId"))
	if err != nil {
		rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "invalid room id", err, nil)
		c.JSON(http.StatusBadRequest, rd)
		return
	}

	access, err := office.CanAccess(requester.OrgID, requester.UserID, ref)
	if err != nil {
		base.fail(c, "failed to check room access", err)
		return
	}
	if !access.CanEnter {
		rd := utility.BuildErrorResponse(http.StatusForbidden, "error", "you cannot view this room", nil, nil)
		c.JSON(http.StatusForbidden, rd)
		return
	}

	occupants, err := office.Occupants(requester.OrgID, ref)
	if err != nil {
		base.fail(c, "failed to load the room occupants", err)
		return
	}

	rd := utility.BuildSuccessResponse(http.StatusOK, "room occupants", occupants)
	c.JSON(http.StatusOK, rd)
}

// EnterRoom records that the requester entered a room and returns the
// realtime channels they may subscribe to.
func (base *Controller) EnterRoom(c *gin.Context) {
	requester, ok := base.authorise(c)
	if !ok {
		return
	}

	office := base.service()

	ref, err := service.ParseRoomID(c.Param("roomId"))
	if err != nil {
		rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "invalid room id", err, nil)
		c.JSON(http.StatusBadRequest, rd)
		return
	}

	access, err := office.CanAccess(requester.OrgID, requester.UserID, ref)
	if err != nil {
		base.fail(c, "failed to check room access", err)
		return
	}
	if !access.CanEnter {
		rd := utility.BuildErrorResponse(http.StatusForbidden, "error", "you cannot enter this room", nil, nil)
		c.JSON(http.StatusForbidden, rd)
		return
	}

	roomID := service.EncodeRoomID(ref.Kind, ref.Ref)

	// A room has a capacity: the focus pod holds one person, a meeting room
	// holds a handful. The room is full once the people inside reach it.
	occupants, err := office.Occupants(requester.OrgID, ref)
	if err != nil {
		base.fail(c, "failed to load the room occupants", err)
		return
	}

	capacity := office.RoomCapacity(requester.OrgID, ref)
	inside := 0
	for _, occupant := range occupants {
		if occupant.UserID != requester.UserID {
			inside++
		}
	}
	if capacity > 0 && inside >= capacity {
		rd := utility.BuildErrorResponse(http.StatusForbidden, "room_full", "this room is full", nil, nil)
		c.JSON(http.StatusForbidden, rd)
		return
	}

	avatar, err := office.GetAvatar(requester.UserID)
	if err != nil {
		base.fail(c, "failed to load your avatar", err)
		return
	}

	presence := models.OfficeOccupantResponse{
		UserID:      requester.UserID,
		DisplayName: office.DisplayName(requester.OrgID, requester.UserID),
		Avatar:      avatar,
		RoomID:      roomID,
		Status:      "online",
		UpdatedAt:   time.Now().UTC(),
	}

	if err := office.JoinRoom(models.OfficePresenceCommand{
		OrganisationID: requester.OrgID,
		Room:           ref,
		UserID:         requester.UserID,
		Presence:       presence,
	}); err != nil {
		base.fail(c, "failed to enter the room", err)
		return
	}

	target := service.NewChannelTarget(requester.OrgID, ref)
	payload := map[string]any{
		"roomId":   roomID,
		"presence": presence,
		"channels": []string{
			service.PresenceChannelName(target),
			service.LayoutChannelName(target),
		},
		"access": access,
	}

	rd := utility.BuildSuccessResponse(http.StatusOK, "room entered", payload)
	c.JSON(http.StatusOK, rd)
}

// LeaveRoom records that the requester left a room.
func (base *Controller) LeaveRoom(c *gin.Context) {
	requester, ok := base.authorise(c)
	if !ok {
		return
	}

	office := base.service()

	ref, err := service.ParseRoomID(c.Param("roomId"))
	if err != nil {
		rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "invalid room id", err, nil)
		c.JSON(http.StatusBadRequest, rd)
		return
	}

	access, err := office.CanAccess(requester.OrgID, requester.UserID, ref)
	if err != nil {
		base.fail(c, "failed to check room access", err)
		return
	}
	if !access.CanEnter {
		rd := utility.BuildErrorResponse(http.StatusForbidden, "error", "you cannot enter this room", nil, nil)
		c.JSON(http.StatusForbidden, rd)
		return
	}

	if err := office.LeaveRoom(requester.OrgID, requester.UserID, ref); err != nil {
		base.fail(c, "failed to leave the room", err)
		return
	}

	rd := utility.BuildSuccessResponse(http.StatusOK, "room left", map[string]any{
		"roomId": service.EncodeRoomID(ref.Kind, ref.Ref),
	})
	c.JSON(http.StatusOK, rd)
}

// GetAvatar returns the office avatar of the requester.
func (base *Controller) GetAvatar(c *gin.Context) {
	requester, ok := base.authorise(c)
	if !ok {
		return
	}

	avatar, err := base.service().GetAvatar(requester.UserID)
	if err != nil {
		base.fail(c, "failed to load your avatar", err)
		return
	}

	rd := utility.BuildSuccessResponse(http.StatusOK, "avatar", avatar)
	c.JSON(http.StatusOK, rd)
}

// SaveAvatar stores the office avatar of the requester.
func (base *Controller) SaveAvatar(c *gin.Context) {
	requester, ok := base.authorise(c)
	if !ok {
		return
	}

	var request models.OfficeAvatarRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		rd := utility.BuildErrorResponse(http.StatusBadRequest, "error", "invalid request body", err, nil)
		c.JSON(http.StatusBadRequest, rd)
		return
	}

	avatar := models.OfficeAvatarConfig{
		Tint:      request.Tint,
		Body:      request.Body,
		Outfit:    request.Outfit,
		Accessory: request.Accessory,
	}

	if err := base.service().SaveAvatar(requester.UserID, avatar); err != nil {
		base.fail(c, "failed to save your avatar", err)
		return
	}

	rd := utility.BuildSuccessResponse(http.StatusOK, "avatar saved", avatar)
	c.JSON(http.StatusOK, rd)
}


