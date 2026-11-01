package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// The Office (3D virtual office) domain.
//
// The building IS the organisation: rooms are derived from channels, DMs and
// users, and only versioned furniture lives in a table of its own. See the
// PRD sections 4.5, 6.1, 6.2 and 6.3.

const (
	OfficeRoomKindLobby     = "lobby"
	OfficeRoomKindReception = "reception"
	OfficeRoomKindLounge    = "lounge"
	OfficeRoomKindMeeting   = "meeting"
	OfficeRoomKindFocus     = "focus"
	OfficeRoomKindChannel   = "channel"
	OfficeRoomKindPersonal  = "personal"
	OfficeRoomKindArea      = "area"
)

// The per-floor extras. They are ordinary rooms on a member floor, not desks,
// so nobody is allocated one.
const (
	OfficeAreaVisitors = "visitors"
	OfficeAreaPods     = "pods"
	OfficeAreaGames    = "games"
)

const (
	OfficeLayoutStatusDraft      = "draft"
	OfficeLayoutStatusPublished  = "published"
	OfficeLayoutStatusSuperseded = "superseded"
)

const (
	// OfficeFloorGeneral is the floor every shared room sits on: the general
	// floor at the top of the tower, holding every channel in the org.
	OfficeFloorGeneral = 0
	// OfficeFloorMember is the top member floor, directly below the general
	// floor. The earliest joiners sit here.
	OfficeFloorMember = 1
	// OfficeFloorCapacity is the number of private offices on a member floor.
	// A floor holds exactly this many and the tower grows downwards.
	OfficeFloorCapacity = 40
)

const (
	OfficeFloorKindGeneral = "general"
	OfficeFloorKindMember  = "member"
)

// OfficeRoomRef is the parsed form of the opaque composite room id used by
// every office route, e.g. "channel:ch_1", "personal:usr_2", "meeting:1".
type OfficeRoomRef struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// OfficeBounds is the 2D footprint of a room on the building grid.
type OfficeBounds struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// OfficeGrid describes the tile system a layout is authored against.
type OfficeGrid struct {
	Width  int     `json:"width"`
	Height int     `json:"height"`
	Tile   float64 `json:"tile"`
}

// OfficeFurnitureItem is a single piece of furniture inside a room layout.
type OfficeFurnitureItem struct {
	ID        string  `json:"id"`
	CatalogID string  `json:"catalogId"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Rotation  int     `json:"rotation"`
	Variant   string  `json:"variant,omitempty"`
}

// OfficeLayoutPayload is the JSON document stored in office_layouts.layout.
type OfficeLayoutPayload struct {
	Grid  OfficeGrid            `json:"grid"`
	Items []OfficeFurnitureItem `json:"items"`
}

// Scan implements sql.Scanner for the jsonb column.
func (p *OfficeLayoutPayload) Scan(value any) error {
	bytes, err := jsonBytes(value)
	if err != nil {
		return fmt.Errorf("failed to unmarshal OfficeLayoutPayload: %w", err)
	}
	if len(bytes) == 0 {
		return nil
	}
	return json.Unmarshal(bytes, p)
}

// Value implements driver.Valuer for the jsonb column.
func (p OfficeLayoutPayload) Value() (driver.Value, error) {
	return json.Marshal(p)
}

// OfficeAvatarConfig is the 1:1 avatar configuration stored on profiles.
type OfficeAvatarConfig struct {
	Tint      string `json:"tint"`
	Body      string `json:"body"`
	Outfit    string `json:"outfit"`
	Accessory string `json:"accessory"`
}

// Scan implements sql.Scanner for the jsonb column.
func (a *OfficeAvatarConfig) Scan(value any) error {
	bytes, err := jsonBytes(value)
	if err != nil {
		return fmt.Errorf("failed to unmarshal OfficeAvatarConfig: %w", err)
	}
	if len(bytes) == 0 {
		return nil
	}
	return json.Unmarshal(bytes, a)
}

// Value implements driver.Valuer for the jsonb column.
func (a OfficeAvatarConfig) Value() (driver.Value, error) {
	return json.Marshal(a)
}

// OfficeSettings are the per-organisation office options stored on
// organisations.office_settings.
type OfficeSettings struct {
	Enabled  bool   `json:"enabled"`
	Theme    string `json:"theme,omitempty"`
	Capacity int    `json:"capacity,omitempty"`
}

// OfficeStatusResponse is the payload of GET /office/status.
type OfficeStatusResponse struct {
	Enabled bool `json:"enabled"`
}

// OfficeSettingsRequest is the body of PUT /office/settings.
type OfficeSettingsRequest struct {
	Enabled *bool `json:"enabled" validate:"required"`
}

// Scan implements sql.Scanner for the jsonb column.
func (s *OfficeSettings) Scan(value any) error {
	bytes, err := jsonBytes(value)
	if err != nil {
		return fmt.Errorf("failed to unmarshal OfficeSettings: %w", err)
	}
	if len(bytes) == 0 {
		return nil
	}
	return json.Unmarshal(bytes, s)
}

// Value implements driver.Valuer for the jsonb column.
func (s OfficeSettings) Value() (driver.Value, error) {
	return json.Marshal(s)
}

// OfficeLayout is one version of a room's furniture.
//
//	office_layouts
//	  id, organisation_id, room_kind, ref_id, version, status, draft_owner_id,
//	  layout, updated_by, published_at, created_at, updated_at
type OfficeLayout struct {
	ID             string              `gorm:"type:uuid;primaryKey;not null" json:"id"`
	OrganisationID string              `gorm:"column:organisation_id;type:uuid;not null;index:idx_office_layouts_room" json:"organisation_id"`
	RoomKind       string              `gorm:"column:room_kind;type:varchar(32);not null;index:idx_office_layouts_room" json:"room_kind"`
	RefID          string              `gorm:"column:ref_id;type:varchar(255);not null;index:idx_office_layouts_room" json:"ref_id"`
	Version        int64               `gorm:"column:version;not null;default:1" json:"version"`
	Status         string              `gorm:"column:status;type:varchar(32);not null;index:idx_office_layouts_room" json:"status"`
	DraftOwnerID   *string             `gorm:"column:draft_owner_id;type:uuid;null" json:"draft_owner_id"`
	Layout         OfficeLayoutPayload `gorm:"column:layout;type:jsonb;not null;default:'{}'" json:"layout"`
	UpdatedBy      string              `gorm:"column:updated_by;type:uuid;not null" json:"updated_by"`
	PublishedAt    *time.Time          `gorm:"column:published_at;null" json:"published_at"`
	CreatedAt      time.Time           `gorm:"column:created_at;not null;autoCreateTime" json:"created_at"`
	UpdatedAt      time.Time           `gorm:"column:updated_at;not null;autoUpdateTime" json:"updated_at"`
}

// OfficeRoomConfig holds the per-room overrides for a derived room. Rows are
// only written for rooms that deviate from the derived default.
type OfficeRoomConfig struct {
	OrganisationID string              `gorm:"column:organisation_id;type:uuid;primaryKey;not null" json:"organisation_id"`
	RoomKind       string              `gorm:"column:room_kind;type:varchar(32);primaryKey;not null" json:"room_kind"`
	RefID          string              `gorm:"column:ref_id;type:varchar(255);primaryKey;not null" json:"ref_id"`
	Bounds         OfficeBounds        `gorm:"column:bounds;type:jsonb" json:"bounds"`
	Capacity       int                 `gorm:"column:capacity;not null;default:8" json:"capacity"`
	Template       OfficeLayoutPayload `gorm:"column:template;type:jsonb" json:"template"`
	Enabled        bool                `gorm:"column:enabled;not null;default:true" json:"enabled"`
	UpdatedBy      string              `gorm:"column:updated_by;type:uuid;not null" json:"updated_by"`
	CreatedAt      time.Time           `gorm:"column:created_at;not null;autoCreateTime" json:"created_at"`
	UpdatedAt      time.Time           `gorm:"column:updated_at;not null;autoUpdateTime" json:"updated_at"`
}

// ---------------------------------------------------------------------------
// API payloads (mirrors the shared schema in zedu-fe/src/types/office.ts)
// ---------------------------------------------------------------------------

// OfficeRoomResponse is a single room in the building directory.
type OfficeRoomResponse struct {
	ID         string       `json:"id"`
	Kind       string       `json:"kind"`
	Name       string       `json:"name"`
	RefID      string       `json:"refId,omitempty"`
	Capacity   int          `json:"capacity"`
	IsLocked   bool         `json:"isLocked"`
	FloorIndex int          `json:"floorIndex"`
	Bounds     OfficeBounds `json:"bounds"`
}

// OfficeLayoutResponse is a published or draft layout for one room.
type OfficeLayoutResponse struct {
	RoomID    string                `json:"roomId"`
	Version   int64                 `json:"version"`
	Status    string                `json:"status"`
	Grid      OfficeGrid            `json:"grid"`
	Items     []OfficeFurnitureItem `json:"items"`
	UpdatedAt time.Time             `json:"updatedAt"`
	UpdatedBy string                `json:"updatedBy,omitempty"`
}

// OfficeBuildingResponse describes the building a requester is standing in.
type OfficeBuildingResponse struct {
	ID    string `json:"id"`
	OrgID string `json:"orgId"`
	Name  string `json:"name"`
}

// OfficeBootstrapResponse is the payload of GET /office/bootstrap.
// OfficeFloorResponse is one storey in the tower directory. The general floor
// is index zero, above every member floor.
type OfficeFloorResponse struct {
	Index     int    `json:"index"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Capacity  int    `json:"capacity"`
	Members   int    `json:"members"`
	HasPortal bool   `json:"hasPortal"`
}

// OfficeMemberResponse is one person in the organisation's office directory.
// It is the tower's source of identity: every member has a floor, an office
// room and the same display name and portrait they use in the 2D client,
// whether or not the requester may walk into that particular office.
type OfficeMemberResponse struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
	Username    string `json:"username,omitempty"`
	Title       string `json:"title,omitempty"`
	AvatarURL   string `json:"avatarUrl,omitempty"`
	Tint        string `json:"tint,omitempty"`
	Role        string `json:"role,omitempty"`
	FloorIndex  int    `json:"floorIndex"`
	RoomID      string `json:"roomId"`
	Online      bool   `json:"online"`
	Status      string `json:"status"`
	StatusText  string `json:"statusText,omitempty"`
	CanVisit    bool   `json:"canVisit"`
}

type OfficeBootstrapResponse struct {
	Building       OfficeBuildingResponse          `json:"building"`
	Settings       OfficeSettings                  `json:"settings"`
	Floors         []OfficeFloorResponse           `json:"floors"`
	Members        []OfficeMemberResponse          `json:"members"`
	Rooms          []OfficeRoomResponse            `json:"rooms"`
	Layouts        map[string]OfficeLayoutResponse `json:"layouts"`
	ChannelRoomMap map[string]string               `json:"channelRoomMap"`
	DMOfficeMap    map[string]string               `json:"dmOfficeMap"`
}

// OfficeLayoutRequest is the body used to save a draft or autosave a personal
// office layout.
type OfficeLayoutRequest struct {
	Grid  OfficeGrid            `json:"grid" validate:"required"`
	Items []OfficeFurnitureItem `json:"items"`
}

// OfficePublishLayoutRequest publishes a draft. IfMatch carries the version
// the editor was based on so concurrent publishes cannot silently overwrite.
type OfficePublishLayoutRequest struct {
	Version int64 `json:"version"`
}

// OfficeAvatarRequest updates the requester's office avatar.
type OfficeAvatarRequest struct {
	Tint      string `json:"tint" validate:"required,hexcolor"`
	Body      string `json:"body" validate:"required,oneof=a b c"`
	Outfit    string `json:"outfit" validate:"required,oneof=casual smart hoodie lab"`
	Accessory string `json:"accessory" validate:"required,oneof=none headphones glasses cap"`
}

// OfficeOccupantResponse is a member currently present in a room.
type OfficeOccupantResponse struct {
	UserID      string             `json:"userId"`
	DisplayName string             `json:"displayName"`
	Username    string             `json:"username"`
	Avatar      OfficeAvatarConfig `json:"avatar"`
	RoomID      string             `json:"roomId"`
	Status      string             `json:"status"`
	Position    OfficeVector       `json:"position"`
	Rotation    float64            `json:"rotation"`
	UpdatedAt   time.Time          `json:"updatedAt"`
}

// OfficeVector is a position inside the building.
type OfficeVector struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// jsonBytes normalises the value a driver hands to a jsonb column. A nullable
// column arrives as nil and must not fail the whole query.
func jsonBytes(value any) ([]byte, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case []byte:
		return typed, nil
	case string:
		return []byte(typed), nil
	default:
		return nil, fmt.Errorf("value is not a json document")
	}
}

// OfficeLayoutCommand carries everything a layout write needs. It keeps the
// service signatures inside the three parameter limit.
type OfficeLayoutCommand struct {
	OrganisationID string
	Room           OfficeRoomRef
	ActorID        string
	Payload        OfficeLayoutPayload
	Version        int64
}

// OfficePresenceCommand carries a presence update for a room.
type OfficePresenceCommand struct {
	OrganisationID string
	Room           OfficeRoomRef
	UserID         string
	Presence       OfficeOccupantResponse
}

// OfficePresenceUpdateRequest is the body of
// POST /office/rooms/{roomId}/presence. A missing position is a malformed
// body, so it is a pointer and required; the bounds themselves are checked
// against the building footprint, where 0 is a valid coordinate.
type OfficePresenceUpdateRequest struct {
	Position *OfficeVector `json:"position" validate:"required"`
	Rotation float64       `json:"rotation"`
}

// OfficePresenceUpdateCommand carries a movement update for a room. An
// update never creates an occupant; only entering does.
type OfficePresenceUpdateCommand struct {
	OrganisationID string
	Room           OfficeRoomRef
	UserID         string
	Position       OfficeVector
	Rotation       float64
}

// ---------------------------------------------------------------------------
// Office telemetry
// ---------------------------------------------------------------------------

// The kinds the ingest endpoint recognises. A client may send a newer kind
// than this server knows about, so an unlisted kind is skipped rather than
// rejected: the batch still succeeds and the frontend can ship first.
const (
	OfficeEventKindSessionStart = "session.start"
	OfficeEventKindSessionEnd   = "session.end"
	OfficeEventKindRoomEnter    = "room.enter"
	OfficeEventKindRoomLeave    = "room.leave"
	OfficeEventKindFloorEnter   = "floor.enter"
	OfficeEventKindPortalCross  = "portal.cross"
)

// OfficeVisitEndReasonSessionEnd closes a visit from the last event of a
// session, which is what the client's flush on pagehide delivers.
const OfficeVisitEndReasonSessionEnd = "session_end"

// OfficeTelemetryModeLive is the only session mode the ingest writes. The
// demo office runs on a synthetic identity with no real visitor, so its
// movements are not evidence about anyone and stay out of these tables.
const OfficeTelemetryModeLive = "live"

// OfficeVisit is one continuous stay inside The Office. It is created by the
// first batch of a session and closed by its session.end event. Durations are
// measured by the server from the event stream, never from client counters.
//
//	office_visits
//	  id, organisation_id, user_id, session_id, mode, entry_point,
//	  started_at, ended_at, duration_ms, active_ms,
//	  rooms_visited, floors_visited, end_reason, created_at, updated_at
type OfficeVisit struct {
	ID             string     `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrganisationID string     `gorm:"column:organisation_id;type:uuid;not null;uniqueIndex:idx_office_visits_org_user_session,priority:1" json:"organisation_id"`
	UserID         string     `gorm:"column:user_id;type:uuid;not null;index;uniqueIndex:idx_office_visits_org_user_session,priority:2" json:"user_id"`
	SessionID      string     `gorm:"column:session_id;type:varchar(64);not null;uniqueIndex:idx_office_visits_org_user_session,priority:3" json:"session_id"`
	Mode           string     `gorm:"column:mode;type:varchar(16);not null" json:"mode"`
	EntryPoint     string     `gorm:"column:entry_point;type:varchar(32)" json:"entry_point"`
	StartedAt      time.Time  `gorm:"column:started_at;not null" json:"started_at"`
	EndedAt        *time.Time `gorm:"column:ended_at;null" json:"ended_at"`
	DurationMs     int64      `gorm:"column:duration_ms;not null;default:0" json:"duration_ms"`
	ActiveMs       int64      `gorm:"column:active_ms;not null;default:0" json:"active_ms"`
	RoomsVisited   int        `gorm:"column:rooms_visited;not null;default:0" json:"rooms_visited"`
	FloorsVisited  int        `gorm:"column:floors_visited;not null;default:0" json:"floors_visited"`
	EndReason      string     `gorm:"column:end_reason;type:varchar(16)" json:"end_reason"`
	CreatedAt      time.Time  `gorm:"column:created_at;not null;autoCreateTime" json:"created_at"`
	UpdatedAt      time.Time  `gorm:"column:updated_at;not null;autoUpdateTime" json:"updated_at"`
}

// OfficeEvent is one thing that happened inside a visit. Rows are append only
// and deduplicated on the client generated event id, so a retried batch
// cannot double count. The corridor is a room with no kind and no reference,
// never a synthetic room. The composite read indexes are not declared here:
// they are created as SQL in migrations.OfficeIndexes, the way the office
// tables already do it.
//
//	office_events
//	  id, event_id, organisation_id, user_id, session_id, kind,
//	  floor_index, room_kind, room_ref, occurred_at, received_at,
//	  dwell_ms, via
type OfficeEvent struct {
	ID             uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	EventID        string    `gorm:"column:event_id;type:uuid;not null;uniqueIndex" json:"event_id"`
	OrganisationID string    `gorm:"column:organisation_id;type:uuid;not null" json:"organisation_id"`
	UserID         string    `gorm:"column:user_id;type:uuid;not null" json:"user_id"`
	SessionID      string    `gorm:"column:session_id;type:varchar(64);not null;index" json:"session_id"`
	Kind           string    `gorm:"column:kind;type:varchar(32);not null" json:"kind"`
	FloorIndex     int       `gorm:"column:floor_index;not null;default:0" json:"floor_index"`
	RoomKind       string    `gorm:"column:room_kind;type:varchar(32)" json:"room_kind"`
	RoomRef        string    `gorm:"column:room_ref;type:varchar(255)" json:"room_ref"`
	OccurredAt     time.Time `gorm:"column:occurred_at;not null" json:"occurred_at"`
	ReceivedAt     time.Time `gorm:"column:received_at;not null" json:"received_at"`
	DwellMs        int64     `gorm:"column:dwell_ms;not null;default:0" json:"dwell_ms"`
	Via            string    `gorm:"column:via;type:varchar(24)" json:"via"`
}

// OfficeTelemetrySession describes the visit a batch belongs to. The ids of
// the caller are never taken from this payload: the organisation and the user
// come from the token claims.
type OfficeTelemetrySession struct {
	SessionID  string    `json:"sessionId"`
	Mode       string    `json:"mode"`
	EntryPoint string    `json:"entryPoint"`
	StartedAt  time.Time `json:"startedAt"`
}

// OfficeTelemetryEvent is one event in a batch. OffsetMs is the milliseconds
// between the session start and the event, which keeps the ordering honest
// even when the client clock is wrong; OccurredAt is derived from it by the
// server.
type OfficeTelemetryEvent struct {
	EventID    string `json:"eventId"`
	Kind       string `json:"kind"`
	OffsetMs   int64  `json:"offsetMs"`
	FloorIndex int    `json:"floorIndex"`
	RoomKind   string `json:"roomKind"`
	RoomRef    string `json:"roomRef"`
	DwellMs    int64  `json:"dwellMs"`
	Via        string `json:"via"`
}

// OfficeTelemetryRequest is the body of POST /office/telemetry.
type OfficeTelemetryRequest struct {
	Session OfficeTelemetrySession `json:"session"`
	Events  []OfficeTelemetryEvent `json:"events"`
}

// OfficeTelemetryCommand carries one ingest call. The organisation and user
// are resolved from the token by the handler and never read from the body, so
// a client cannot record events against someone else's organisation.
type OfficeTelemetryCommand struct {
	OrganisationID string
	UserID         string
	Request        OfficeTelemetryRequest
}

// OfficeTelemetryResponse is the payload of a successful ingest. Accepted
// counts rows written now, so a retried batch reports zero accepted and the
// client can tell a duplicate from a loss.
type OfficeTelemetryResponse struct {
	Accepted   int `json:"accepted"`
	Duplicated int `json:"duplicated"`
	Ignored    int `json:"ignored"`
}
