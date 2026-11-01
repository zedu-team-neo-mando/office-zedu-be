package office

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/hngprojects/telex_be/internal/models"
)

// Telemetry limits. They are deliberately tight: the endpoint is written by a
// browser that can be told to send anything, so nothing is trusted past these.
const (
	// MaxTelemetryEvents is the largest batch one request may carry.
	MaxTelemetryEvents = 50

	// MaxTelemetryBodyBytes bounds the raw request body before it is parsed.
	MaxTelemetryBodyBytes = 32 * 1024

	// maxTelemetrySessionID matches the varchar(64) column.
	maxTelemetrySessionID = 64

	// maxTelemetryEntryPoint matches the varchar(32) column.
	maxTelemetryEntryPoint = 32

	// maxTelemetryFloorIndex caps the floor a client may claim. The tower
	// grows one storey per 40 members, so this is far above any real building
	// and only stops a wild number from being stored.
	maxTelemetryFloorIndex = 4096

	// maxTelemetryRoomRef matches the varchar(255) column.
	maxTelemetryRoomRef = 255

	// maxTelemetryVia matches the varchar(24) column.
	maxTelemetryVia = 24

	// maxTelemetryDwell rejects a dwell longer than six hours. A tab left
	// open overnight is not six hours of work in a room.
	maxTelemetryDwell = 6 * time.Hour

	// maxTelemetryOffset bounds how far an event may sit from its own session
	// start, which caps how wrong a client clock can push occurred_at.
	maxTelemetryOffset = 24 * time.Hour

	// maxTelemetryStartedSkew is how far a session may claim to have started
	// in the future, absorbing normal client and server clock drift.
	maxTelemetryStartedSkew = 5 * time.Minute

	// telemetryBatchInterval is the smallest gap between two accepted batches
	// from one person.
	telemetryBatchInterval = 2 * time.Second

	// telemetryRateKeyPrefix namespaces the rate-limit keys in Redis.
	telemetryRateKeyPrefix = "office:telemetry:batch:"
)

// ErrTelemetryRateLimited is returned when a person sends faster than the
// ingest allows. The handler answers 429 so the client can back off.
var ErrTelemetryRateLimited = errors.New("telemetry batches are arriving too quickly")

// Telemetry validation errors. Each names what the client did wrong, because
// a silent 400 on a background beacon is otherwise impossible to debug.
var (
	ErrTelemetrySessionID    = errors.New("the telemetry session id is missing or too long")
	ErrTelemetryMode         = errors.New("only live office sessions are recorded")
	ErrTelemetryStartedAt    = errors.New("the telemetry session start is missing or impossible")
	ErrTelemetryEventCount   = errors.New("a telemetry batch must carry between 1 and 50 events")
	ErrTelemetryEventID      = errors.New("a telemetry event is missing a valid event id")
	ErrTelemetryOffset       = errors.New("a telemetry event is outside its session window")
	ErrTelemetryDwell        = errors.New("a telemetry event carries an impossible dwell time")
	ErrTelemetryRoom         = errors.New("a telemetry event names an unknown room")
	ErrTelemetryFloor        = errors.New("a telemetry event carries an impossible floor index")
	ErrTelemetryViaTooLong   = errors.New("a telemetry event carries an over-long travel source")
	ErrTelemetryOrgID        = errors.New("the organisation id is not valid")
	ErrTelemetryUserIDAbsent = errors.New("a live telemetry event requires the requester user id")
)

// telemetryValidationErrors is every failure that is the client's fault rather
// than the server's. The handler turns them into 422 instead of 500, so a bug
// in the collector never looks like an outage.
var telemetryValidationErrors = []error{
	ErrTelemetrySessionID,
	ErrTelemetryMode,
	ErrTelemetryStartedAt,
	ErrTelemetryEventCount,
	ErrTelemetryEventID,
	ErrTelemetryOffset,
	ErrTelemetryDwell,
	ErrTelemetryRoom,
	ErrTelemetryFloor,
	ErrTelemetryViaTooLong,
	ErrTelemetryOrgID,
	ErrTelemetryUserIDAbsent,
}

// IsTelemetryValidationError reports whether err is a rejected batch rather
// than a storage or transport failure.
func IsTelemetryValidationError(err error) bool {
	for _, candidate := range telemetryValidationErrors {
		if errors.Is(err, candidate) {
			return true
		}
	}
	return false
}

// officeTelemetryModes lists the session modes the ingest writes. Anything
// else, including a blank mode, is refused: the demo office has no real
// session and must never reach the tables that answer "who worked where".
var officeTelemetryModes = map[string]bool{models.OfficeTelemetryModeLive: true}

// officeTelemetryKinds lists the event kinds the ingest writes. A kind that is
// not here is skipped, not rejected, so a newer frontend can ship events this
// server has not learned yet without its batches failing.
var officeTelemetryKinds = map[string]bool{
	models.OfficeEventKindSessionStart: true,
	models.OfficeEventKindSessionEnd:   true,
	models.OfficeEventKindRoomEnter:    true,
	models.OfficeEventKindRoomLeave:    true,
	models.OfficeEventKindFloorEnter:   true,
	models.OfficeEventKindPortalCross:  true,
}

// officeTelemetryRoomKinds reuses the room kinds the office already defines.
// The empty string is allowed separately: it is the corridor, which is a place
// without a room.
var officeTelemetryRoomKinds = map[string]bool{
	models.OfficeRoomKindLobby:     true,
	models.OfficeRoomKindReception: true,
	models.OfficeRoomKindLounge:    true,
	models.OfficeRoomKindMeeting:   true,
	models.OfficeRoomKindFocus:     true,
	models.OfficeRoomKindChannel:   true,
	models.OfficeRoomKindPersonal:  true,
	models.OfficeRoomKindArea:      true,
}

// telemetryBatch is a validated request, ready to write. It is built apart from
// the database so the rules can be tested without a running server.
type telemetryBatch struct {
	SessionID  string
	Mode       string
	EntryPoint string
	StartedAt  time.Time
	OrganisationID string
	UserID         string
	Rows       []models.OfficeEvent
	// Ignored counts events skipped because this server does not know their
	// kind. They are not errors and they are not written.
	Ignored int
	// HasSessionEnd is true when the batch closes the visit, which finalises
	// the aggregate row.
	HasSessionEnd bool
}

// PrepareTelemetry checks a telemetry request and turns it into the rows to
// insert. It rejects the whole batch on a malformed session or an unusable
// event, and silently skips events whose kind this server does not know.
// OccurredAt is derived from the session start plus the event offset, so the
// absolute client clock never decides when something happened.
func PrepareTelemetry(orgID, userID string, req models.OfficeTelemetryRequest) (*telemetryBatch, error) {
	if _, err := uuid.Parse(strings.TrimSpace(userID)); err != nil {
		return nil, ErrTelemetryUserIDAbsent
	}

	if _, err := uuid.Parse(strings.TrimSpace(orgID)); err != nil {
		return nil, ErrTelemetryOrgID
	}

	sessionID := strings.TrimSpace(req.Session.SessionID)
	if sessionID == "" || len(sessionID) > maxTelemetrySessionID {
		return nil, ErrTelemetrySessionID
	}

	mode := strings.TrimSpace(req.Session.Mode)
	if !officeTelemetryModes[mode] {
		return nil, ErrTelemetryMode
	}

	startedAt := req.Session.StartedAt.UTC()
	if startedAt.IsZero() {
		return nil, ErrTelemetryStartedAt
	}

	now := time.Now().UTC()
	if startedAt.After(now.Add(maxTelemetryStartedSkew)) ||
		startedAt.Before(now.Add(-7*24*time.Hour)) {
		return nil, ErrTelemetryStartedAt
	}

	if len(req.Events) == 0 || len(req.Events) > MaxTelemetryEvents {
		return nil, ErrTelemetryEventCount
	}

	entryPoint := strings.TrimSpace(req.Session.EntryPoint)
	if len(entryPoint) > maxTelemetryEntryPoint {
		entryPoint = entryPoint[:maxTelemetryEntryPoint]
	}

	batch := &telemetryBatch{
		SessionID:        sessionID,
		Mode:             mode,
		EntryPoint:       entryPoint,
		StartedAt:        startedAt,
		OrganisationID:   orgID,
		UserID:           userID,
		Rows:             make([]models.OfficeEvent, 0, len(req.Events)),
	}

	// The same event id twice in one batch would be counted once by the
	// database, so drop the repeat here and keep the reported count honest.
	seen := make(map[string]bool, len(req.Events))
	receivedAt := now

	for index, event := range req.Events {
		kind := strings.TrimSpace(event.Kind)
		if !officeTelemetryKinds[kind] {
			batch.Ignored++
			continue
		}

		eventID, err := uuid.Parse(strings.TrimSpace(event.EventID))
		if err != nil {
			return nil, fmt.Errorf("%w: event %d", ErrTelemetryEventID, index)
		}

		if seen[eventID.String()] {
			continue
		}

		if event.OffsetMs < 0 || event.OffsetMs > maxTelemetryOffset.Milliseconds() {
			return nil, fmt.Errorf("%w: event %d", ErrTelemetryOffset, index)
		}

		if event.DwellMs < 0 || event.DwellMs > maxTelemetryDwell.Milliseconds() {
			return nil, fmt.Errorf("%w: event %d", ErrTelemetryDwell, index)
		}

		if event.FloorIndex < 0 || event.FloorIndex > maxTelemetryFloorIndex {
			return nil, fmt.Errorf("%w: event %d", ErrTelemetryFloor, index)
		}

		roomKind := strings.TrimSpace(event.RoomKind)
		roomRef := strings.TrimSpace(event.RoomRef)

		// An empty kind is the corridor, which carries no reference either.
		if roomKind != "" && !officeTelemetryRoomKinds[roomKind] {
			return nil, fmt.Errorf("%w: event %d", ErrTelemetryRoom, index)
		}
		if roomKind == "" && roomRef != "" {
			return nil, fmt.Errorf("%w: event %d", ErrTelemetryRoom, index)
		}
		if len(roomRef) > maxTelemetryRoomRef {
			return nil, fmt.Errorf("%w: event %d", ErrTelemetryRoom, index)
		}

		via := strings.TrimSpace(event.Via)
		if len(via) > maxTelemetryVia {
			return nil, fmt.Errorf("%w: event %d", ErrTelemetryViaTooLong, index)
		}

		if kind == models.OfficeEventKindSessionEnd {
			batch.HasSessionEnd = true
		}

		seen[eventID.String()] = true
		batch.Rows = append(batch.Rows, models.OfficeEvent{
			EventID:        eventID.String(),
			OrganisationID: orgID,
			UserID:         userID,
			SessionID:      sessionID,
			Kind:           kind,
			FloorIndex:     event.FloorIndex,
			RoomKind:       roomKind,
			RoomRef:        roomRef,
			OccurredAt:     startedAt.Add(time.Duration(event.OffsetMs) * time.Millisecond),
			ReceivedAt:     receivedAt,
			DwellMs:        event.DwellMs,
			Via:            via,
		})
	}

	return batch, nil
}

// IngestTelemetry writes one validated batch. The visit row is created by the
// first batch of a session and left alone afterwards, events are inserted with
// ON CONFLICT DO NOTHING so a retried batch cannot double count, and the
// aggregates are recomputed from the stored events rather than from anything
// the client claimed. It runs in one transaction and answers 202 through the
// handler.
func (s *Service) IngestTelemetry(cmd models.OfficeTelemetryCommand) (*models.OfficeTelemetryResponse, error) {
	batch, err := PrepareTelemetry(cmd.OrganisationID, cmd.UserID, cmd.Request)
	if err != nil {
		return nil, err
	}

	if err := s.telemetryRateLimit(cmd.OrganisationID, cmd.UserID); err != nil {
		return nil, err
	}

	response := &models.OfficeTelemetryResponse{Ignored: batch.Ignored}

	if len(batch.Rows) == 0 {
		return response, nil
	}

	err = s.Db.Postgresql.Transaction(func(tx *gorm.DB) error {
		if err := insertTelemetryVisit(tx, batch); err != nil {
			return err
		}

		accepted, err := insertTelemetryEvents(tx, batch.Rows)
		if err != nil {
			return err
		}

		response.Accepted = accepted
		response.Duplicated = len(batch.Rows) - accepted

		return refreshTelemetryVisit(tx, cmd.OrganisationID, cmd.UserID, batch.SessionID)
	})
	if err != nil {
		return nil, err
	}

	return response, nil
}

// telemetryRateLimit allows one batch per person per interval. SET NX is a
// single atomic command, so two retries from the same tab cannot both be
// accepted. Without Redis the limit is skipped rather than blocking ingest,
// which matches how presence throttling behaves.
func (s *Service) telemetryRateLimit(orgID, userID string) error {
	if s.Db.Redis == nil {
		return nil
	}

	key := telemetryRateKeyPrefix + orgID + ":" + userID
	accepted, err := s.Db.Redis.SetNX(context.Background(), key, time.Now().UnixNano(), telemetryBatchInterval).Result()
	if err != nil {
		return err
	}

	if !accepted {
		return ErrTelemetryRateLimited
	}

	return nil
}

// insertTelemetryVisit creates the visit row on the first batch of a session.
// A later batch collides on the unique (organisation_id, user_id, session_id)
// pair and changes nothing, so the recorded start stays the one from
// session.start. The owner is read from batch, not passed positionally,
// keeping the function inside the three-parameter limit.
func insertTelemetryVisit(tx *gorm.DB, batch *telemetryBatch) error {
	query := `
	INSERT INTO office_visits
		(organisation_id, user_id, session_id, mode, entry_point, started_at)
	VALUES
		($1, $2, $3, $4, $5, $6)
	ON CONFLICT (organisation_id, user_id, session_id) DO NOTHING`

	if err := tx.Exec(query,
		batch.OrganisationID,
		batch.UserID,
		batch.SessionID,
		batch.Mode,
		batch.EntryPoint,
		batch.StartedAt,
	).Error; err != nil {
		return fmt.Errorf("could not record the office visit: %w", err)
	}

	return nil
}

// insertTelemetryEvents writes the batch in one statement. The event id is
// unique, so a resend lands on ON CONFLICT DO NOTHING and RowsAffected counts
// only the rows that were actually written.
func insertTelemetryEvents(tx *gorm.DB, rows []models.OfficeEvent) (int, error) {
	columns := `event_id, organisation_id, user_id, session_id, kind,
		floor_index, room_kind, room_ref, occurred_at, received_at, dwell_ms, via`

	values := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*12)

	for index, row := range rows {
		// Twelve placeholders per row, numbered across the whole statement.
		base := index * 12
		values = append(values, fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
			base+1, base+2, base+3, base+4, base+5, base+6,
			base+7, base+8, base+9, base+10, base+11, base+12))

		args = append(args,
			row.EventID,
			row.OrganisationID,
			row.UserID,
			row.SessionID,
			row.Kind,
			row.FloorIndex,
			row.RoomKind,
			row.RoomRef,
			row.OccurredAt,
			row.ReceivedAt,
			row.DwellMs,
			row.Via,
		)
	}

	query := fmt.Sprintf(
		"INSERT INTO office_events (%s) VALUES %s ON CONFLICT (event_id) DO NOTHING",
		columns, strings.Join(values, ","))

	result := tx.Exec(query, args...)
	if result.Error != nil {
		return 0, fmt.Errorf("could not record the office events: %w", result.Error)
	}

	return int(result.RowsAffected), nil
}

// telemetryAggregate is what the stored events say about one session. It is
// read back from the database instead of trusting the client's counters.
type telemetryAggregate struct {
	Rooms  int64        `gorm:"column:rooms"`
	Floors int64        `gorm:"column:floors"`
	Dwell  int64        `gorm:"column:dwell"`
	Ended  sql.NullTime `gorm:"column:ended_at"`
}

// refreshTelemetryVisit recomputes the visit from its events. Rooms counted
// exclude the corridor, dwell is the sum of the room stays the client timed,
// and the visit is closed with a duration only once a session.end is stored.
// The visit row is locked with FOR UPDATE before the aggregate read so that
// overlapping batches for one session cannot overwrite each other's totals.
func refreshTelemetryVisit(tx *gorm.DB, orgID, userID, sessionID string) error {
	lock := `SELECT 1 FROM office_visits
	WHERE organisation_id = $1 AND user_id = $2 AND session_id = $3 FOR UPDATE`
	if err := tx.Exec(lock, orgID, userID, sessionID).Error; err != nil {
		return fmt.Errorf("could not lock the office visit: %w", err)
	}

	read := `
	SELECT
		COUNT(DISTINCT (room_kind, room_ref))
			FILTER (WHERE kind = 'room.enter' AND room_kind <> '')::bigint AS rooms,
		COUNT(DISTINCT floor_index)
			FILTER (WHERE kind = 'floor.enter')::bigint AS floors,
		COALESCE(SUM(dwell_ms) FILTER (WHERE kind = 'room.leave'), 0)::bigint AS dwell,
		MAX(occurred_at) FILTER (WHERE kind = 'session.end') AS ended_at
	FROM office_events
	WHERE organisation_id = $1 AND user_id = $2 AND session_id = $3`

	var aggregate telemetryAggregate
	if err := tx.Raw(read, orgID, userID, sessionID).Scan(&aggregate).Error; err != nil {
		return fmt.Errorf("could not total the office events: %w", err)
	}

	// Duration and end reason are only written when the visit is closed, so an
	// open visit keeps the null ended_at it started with.
	write := `
	UPDATE office_visits
	SET rooms_visited  = $1,
	    floors_visited = $2,
	    active_ms      = $3,
	    ended_at       = CASE WHEN $4::timestamptz IS NULL THEN ended_at ELSE $4::timestamptz END,
	    duration_ms    = CASE WHEN $4::timestamptz IS NULL THEN duration_ms
	                         ELSE GREATEST(FLOOR(EXTRACT(EPOCH FROM ($4::timestamptz - started_at)) * 1000)::bigint, 0) END,
	    end_reason     = CASE WHEN $4::timestamptz IS NULL THEN end_reason ELSE $5 END
	WHERE organisation_id = $6 AND user_id = $7 AND session_id = $8`

	var endedAt any
	if aggregate.Ended.Valid {
		endedAt = aggregate.Ended.Time
	}

	if err := tx.Exec(write,
		aggregate.Rooms,
		aggregate.Floors,
		aggregate.Dwell,
		endedAt,
		models.OfficeVisitEndReasonSessionEnd,
		orgID,
		userID,
		sessionID,
	).Error; err != nil {
		return fmt.Errorf("could not update the office visit: %w", err)
	}

	return nil
}
