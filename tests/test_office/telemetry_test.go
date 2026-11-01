package test_office

import (
	"strings"
	"testing"
	"time"

	"github.com/hngprojects/telex_be/internal/models"
	service "github.com/hngprojects/telex_be/services/office"
)

// The ingest rules live in PrepareTelemetry, which touches no database, so the
// whole contract can be checked here. These are the cases a broken collector
// would actually send.

const (
	testOrgID    = "2f7f9b1c-6c4f-4e21-9a1a-0b6f1c2d3e4f"
	testUserID   = "7c1e2d3f-4b5a-4968-8a7b-c9d0e1f2a3b4"
	testEvent1ID = "11111111-2222-3333-4444-555555555555"
	testEvent2ID = "66666666-7777-8888-9999-000000000000"
)

// validTelemetryRequest builds a batch that passes, so each case only has to
// break one thing.
func validTelemetryRequest() models.OfficeTelemetryRequest {
	return models.OfficeTelemetryRequest{
		Session: models.OfficeTelemetrySession{
			SessionID:  "sess-abc",
			Mode:       models.OfficeTelemetryModeLive,
			EntryPoint: "lobby",
			StartedAt:  time.Now().UTC().Add(-3 * time.Minute),
		},
		Events: []models.OfficeTelemetryEvent{
			{
				EventID:  testEvent1ID,
				Kind:     models.OfficeEventKindSessionStart,
				OffsetMs: 0,
			},
			{
				EventID:    testEvent2ID,
				Kind:       models.OfficeEventKindRoomEnter,
				OffsetMs:   1500,
				FloorIndex: 2,
				RoomKind:   models.OfficeRoomKindChannel,
				RoomRef:    "ch_42",
				Via:        "directory",
			},
		},
	}
}

func TestPrepareTelemetryAcceptsAValidBatch(t *testing.T) {
	request := validTelemetryRequest()

	batch, err := service.PrepareTelemetry(testOrgID, testUserID, request)
	if err != nil {
		t.Fatalf("a well formed batch should pass, got: %v", err)
	}
	if len(batch.Rows) != 2 {
		t.Fatalf("expected 2 stored events, got %d", len(batch.Rows))
	}
	if batch.Ignored != 0 {
		t.Fatalf("no event should be skipped, got Ignored=%d", batch.Ignored)
	}
	if batch.HasSessionEnd {
		t.Fatal("a batch without session.end must not close the visit")
	}

	first := batch.Rows[0]
	if first.EventID != testEvent1ID {
		t.Fatalf("event id should be stored verbatim, got %q", first.EventID)
	}
	if first.OrganisationID != testOrgID || first.UserID != testUserID {
		t.Fatal("organisation and user must come from the caller, not the body")
	}
	if !first.OccurredAt.Equal(batch.StartedAt) {
		t.Fatalf("an offset of 0 should land on the session start, got %v", first.OccurredAt)
	}

	second := batch.Rows[1]
	if !second.OccurredAt.Equal(batch.StartedAt.Add(1500 * time.Millisecond)) {
		t.Fatalf("occurred_at should be the start plus the offset, got %v", second.OccurredAt)
	}
	if second.RoomKind != models.OfficeRoomKindChannel || second.RoomRef != "ch_42" {
		t.Fatalf("the room should be stored as sent, got %q %q", second.RoomKind, second.RoomRef)
	}
	if second.Via != "directory" {
		t.Fatalf("a teleport must keep how the traveller got there, got %q", second.Via)
	}
	if second.ReceivedAt.IsZero() {
		t.Fatal("received_at should be the server clock")
	}
}

// The corridor is a place with no room. It must survive as an empty kind and an
// empty reference rather than being turned into a fake room.
func TestPrepareTelemetryKeepsTheCorridor(t *testing.T) {
	request := validTelemetryRequest()
	request.Events = []models.OfficeTelemetryEvent{
		{EventID: testEvent1ID, Kind: models.OfficeEventKindRoomLeave, DwellMs: 4200},
	}

	batch, err := service.PrepareTelemetry(testOrgID, testUserID, request)
	if err != nil {
		t.Fatalf("a corridor event should pass, got: %v", err)
	}
	if batch.Rows[0].RoomKind != "" || batch.Rows[0].RoomRef != "" {
		t.Fatalf("the corridor must stay empty, got %q %q", batch.Rows[0].RoomKind, batch.Rows[0].RoomRef)
	}
	if batch.Rows[0].DwellMs != 4200 {
		t.Fatalf("the corridor dwell must be accumulated, got %d", batch.Rows[0].DwellMs)
	}
}

func TestPrepareTelemetrySessionEndClosesTheVisit(t *testing.T) {
	request := validTelemetryRequest()
	request.Events = []models.OfficeTelemetryEvent{
		{EventID: testEvent1ID, Kind: models.OfficeEventKindSessionEnd, OffsetMs: 60000},
	}

	batch, err := service.PrepareTelemetry(testOrgID, testUserID, request)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !batch.HasSessionEnd {
		t.Fatal("session.end should mark the batch as closing the visit")
	}
}

// A newer frontend may send a kind this server has not learned. The batch still
// succeeds, so shipping the client first is safe.
func TestPrepareTelemetrySkipsUnknownKinds(t *testing.T) {
	request := validTelemetryRequest()
	request.Events = []models.OfficeTelemetryEvent{
		{EventID: testEvent1ID, Kind: models.OfficeEventKindRoomEnter},
		{EventID: testEvent2ID, Kind: "chair.sat_down", OffsetMs: 10},
	}

	batch, err := service.PrepareTelemetry(testOrgID, testUserID, request)
	if err != nil {
		t.Fatalf("an unknown kind should be skipped, not rejected, got: %v", err)
	}
	if len(batch.Rows) != 1 {
		t.Fatalf("only the known event should be stored, got %d rows", len(batch.Rows))
	}
	if batch.Ignored != 1 {
		t.Fatalf("the skipped event should be counted, got Ignored=%d", batch.Ignored)
	}
}

// The same id twice in one batch would otherwise be reported as two writes.
func TestPrepareTelemetryDeduplicatesWithinABatch(t *testing.T) {
	request := validTelemetryRequest()
	request.Events = []models.OfficeTelemetryEvent{
		{EventID: testEvent1ID, Kind: models.OfficeEventKindRoomEnter},
		{EventID: testEvent1ID, Kind: models.OfficeEventKindRoomEnter},
	}

	batch, err := service.PrepareTelemetry(testOrgID, testUserID, request)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(batch.Rows) != 1 {
		t.Fatalf("a repeated event id should be written once, got %d rows", len(batch.Rows))
	}
}

func TestPrepareTelemetryRejectsBadBatches(t *testing.T) {
	longSession := strings.Repeat("s", 65)
	longRef := strings.Repeat("r", 256)
	longVia := strings.Repeat("v", 25)

	cases := []struct {
		name    string
		mutate  func(*models.OfficeTelemetryRequest)
		wantErr error
	}{
		{
			name:    "no session id",
			mutate:  func(r *models.OfficeTelemetryRequest) { r.Session.SessionID = "  " },
			wantErr: service.ErrTelemetrySessionID,
		},
		{
			name:    "session id too long",
			mutate:  func(r *models.OfficeTelemetryRequest) { r.Session.SessionID = longSession },
			wantErr: service.ErrTelemetrySessionID,
		},
		{
			name:    "demo mode is not recorded",
			mutate:  func(r *models.OfficeTelemetryRequest) { r.Session.Mode = "demo" },
			wantErr: service.ErrTelemetryMode,
		},
		{
			name:    "blank mode",
			mutate:  func(r *models.OfficeTelemetryRequest) { r.Session.Mode = "" },
			wantErr: service.ErrTelemetryMode,
		},
		{
			name:    "missing start",
			mutate:  func(r *models.OfficeTelemetryRequest) { r.Session.StartedAt = time.Time{} },
			wantErr: service.ErrTelemetryStartedAt,
		},
		{
			name: "start in the future",
			mutate: func(r *models.OfficeTelemetryRequest) {
				r.Session.StartedAt = time.Now().UTC().Add(time.Hour)
			},
			wantErr: service.ErrTelemetryStartedAt,
		},
		{
			name:    "no events",
			mutate:  func(r *models.OfficeTelemetryRequest) { r.Events = nil },
			wantErr: service.ErrTelemetryEventCount,
		},
		{
			name: "more than fifty events",
			mutate: func(r *models.OfficeTelemetryRequest) {
				events := make([]models.OfficeTelemetryEvent, 0, service.MaxTelemetryEvents+1)
				for i := 0; i <= service.MaxTelemetryEvents; i++ {
					events = append(events, models.OfficeTelemetryEvent{
						EventID: testEvent1ID,
						Kind:    models.OfficeEventKindFloorEnter,
					})
				}
				r.Events = events
			},
			wantErr: service.ErrTelemetryEventCount,
		},
		{
			name:    "event id is not a uuid",
			mutate:  func(r *models.OfficeTelemetryRequest) { r.Events[0].EventID = "evt-1" },
			wantErr: service.ErrTelemetryEventID,
		},
		{
			name:    "negative offset",
			mutate:  func(r *models.OfficeTelemetryRequest) { r.Events[0].OffsetMs = -1 },
			wantErr: service.ErrTelemetryOffset,
		},
		{
			name: "offset beyond a day",
			mutate: func(r *models.OfficeTelemetryRequest) {
				r.Events[0].OffsetMs = 24*time.Hour.Milliseconds() + 1
			},
			wantErr: service.ErrTelemetryOffset,
		},
		{
			name: "dwell beyond six hours",
			mutate: func(r *models.OfficeTelemetryRequest) {
				r.Events[0].DwellMs = 6*time.Hour.Milliseconds() + 1
			},
			wantErr: service.ErrTelemetryDwell,
		},
		{
			name:    "negative dwell",
			mutate:  func(r *models.OfficeTelemetryRequest) { r.Events[0].DwellMs = -5 },
			wantErr: service.ErrTelemetryDwell,
		},
		{
			name:    "unknown room kind",
			mutate:  func(r *models.OfficeTelemetryRequest) { r.Events[1].RoomKind = "bedroom" },
			wantErr: service.ErrTelemetryRoom,
		},
		{
			name: "reference without a room kind",
			mutate: func(r *models.OfficeTelemetryRequest) {
				r.Events[1].RoomKind = ""
				r.Events[1].RoomRef = "ch_42"
			},
			wantErr: service.ErrTelemetryRoom,
		},
		{
			name:    "room reference too long",
			mutate:  func(r *models.OfficeTelemetryRequest) { r.Events[1].RoomRef = longRef },
			wantErr: service.ErrTelemetryRoom,
		},
		{
			name:    "via too long",
			mutate:  func(r *models.OfficeTelemetryRequest) { r.Events[1].Via = longVia },
			wantErr: service.ErrTelemetryViaTooLong,
		},
		{
			name:    "negative floor",
			mutate:  func(r *models.OfficeTelemetryRequest) { r.Events[1].FloorIndex = -1 },
			wantErr: service.ErrTelemetryFloor,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := validTelemetryRequest()
			testCase.mutate(&request)

			_, err := service.PrepareTelemetry(testOrgID, testUserID, request)
			if err == nil {
				t.Fatal("expected the batch to be rejected")
			}
			if !service.IsTelemetryValidationError(err) {
				t.Fatalf("a rejected batch must be a validation error, got: %v", err)
			}
			if !strings.Contains(err.Error(), testCase.wantErr.Error()) {
				t.Fatalf("expected %v in %q", testCase.wantErr, err.Error())
			}
		})
	}
}

func TestPrepareTelemetryRejectsBadIdentity(t *testing.T) {
	request := validTelemetryRequest()

	if _, err := service.PrepareTelemetry(testOrgID, "not-a-uuid", request); err == nil {
		t.Fatal("a caller without a valid user id must be refused")
	} else if !service.IsTelemetryValidationError(err) {
		t.Fatalf("expected a validation error, got %v", err)
	}

	if _, err := service.PrepareTelemetry("not-a-uuid", testUserID, request); err == nil {
		t.Fatal("a caller without a valid organisation id must be refused")
	} else if !service.IsTelemetryValidationError(err) {
		t.Fatalf("expected a validation error, got %v", err)
	}
}

// A long entry point is trimmed instead of rejected: it is a label, not a key.
func TestPrepareTelemetryTruncatesEntryPoint(t *testing.T) {
	request := validTelemetryRequest()
	request.Session.EntryPoint = strings.Repeat("e", 40)

	batch, err := service.PrepareTelemetry(testOrgID, testUserID, request)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(batch.EntryPoint) != 32 {
		t.Fatalf("entry point should fit the varchar(32) column, got %d characters", len(batch.EntryPoint))
	}
}

func TestRateLimitErrorIsNotAValidationError(t *testing.T) {
	if service.IsTelemetryValidationError(service.ErrTelemetryRateLimited) {
		t.Fatal("being rate limited is not a malformed batch; the handler must answer 429, not 422")
	}
	if service.IsTelemetryValidationError(nil) {
		t.Fatal("no error is not a validation error")
	}
}
