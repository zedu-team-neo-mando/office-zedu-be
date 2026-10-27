package test_office

import (
	"errors"
	"testing"

	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/services/office"
)

func validPayload() models.OfficeLayoutPayload {
	return models.OfficeLayoutPayload{
		Grid: models.OfficeGrid{Width: 56, Height: 36, Tile: 1},
		Items: []models.OfficeFurnitureItem{
			{ID: "item-1", CatalogID: "desk_oak", X: 10, Y: 12, Rotation: 90},
		},
	}
}

func TestValidateLayoutPayload(t *testing.T) {
	if err := office.ValidateLayoutPayload(validPayload()); err != nil {
		t.Fatalf("expected a valid payload, got %v", err)
	}
}

func TestValidateLayoutPayloadRejectsBadInput(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(payload *models.OfficeLayoutPayload)
		wantErr bool
	}{
		{
			name:    "zero width",
			mutate:  func(p *models.OfficeLayoutPayload) { p.Grid.Width = 0 },
			wantErr: true,
		},
		{
			name:    "zero tile",
			mutate:  func(p *models.OfficeLayoutPayload) { p.Grid.Tile = 0 },
			wantErr: true,
		},
		{
			name:    "missing item id",
			mutate:  func(p *models.OfficeLayoutPayload) { p.Items[0].ID = "" },
			wantErr: true,
		},
		{
			name:    "missing catalogue id",
			mutate:  func(p *models.OfficeLayoutPayload) { p.Items[0].CatalogID = "" },
			wantErr: true,
		},
		{
			name: "duplicate item id",
			mutate: func(p *models.OfficeLayoutPayload) {
				p.Items = append(p.Items, p.Items[0])
			},
			wantErr: true,
		},
		{
			name:    "bad rotation",
			mutate:  func(p *models.OfficeLayoutPayload) { p.Items[0].Rotation = 45 },
			wantErr: true,
		},
		{
			name:    "outside the room",
			mutate:  func(p *models.OfficeLayoutPayload) { p.Items[0].X = 500 },
			wantErr: true,
		},
		{
			name: "too many items",
			mutate: func(p *models.OfficeLayoutPayload) {
				items := make([]models.OfficeFurnitureItem, 0, office.MaxItemsPerLayout+1)
				for index := 0; index <= office.MaxItemsPerLayout; index++ {
					items = append(items, models.OfficeFurnitureItem{
						ID:        string(rune('a'+index%26)) + string(rune('a'+index/26)),
						CatalogID: "chair_ergo",
						X:         1,
						Y:         1,
					})
				}
				p.Items = items
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := validPayload()
			tc.mutate(&payload)

			err := office.ValidateLayoutPayload(payload)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				if !errors.Is(err, office.ErrInvalidLayout) {
					t.Fatalf("expected ErrInvalidLayout, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestToResponseFillsEmptyItems(t *testing.T) {
	layout := &models.OfficeLayout{
		RoomKind: models.OfficeRoomKindChannel,
		RefID:    "ch_1",
		Version:  3,
		Status:   models.OfficeLayoutStatusPublished,
	}

	response := office.ToResponse(layout)
	if response.RoomID != "channel:ch_1" {
		t.Fatalf("room id: got %q", response.RoomID)
	}
	if response.Version != 3 {
		t.Fatalf("version: got %d", response.Version)
	}
	if response.Items == nil {
		t.Fatal("items should never be nil")
	}
	if len(response.Items) != 0 {
		t.Fatalf("items: got %d, want 0", len(response.Items))
	}
}

// The draft and layout endpoints both fall back to this response when nothing
// has been saved. It must carry the building grid, not the zero value, or the
// editor receives contradictory defaults depending on which endpoint it calls.
func TestEmptyLayoutResponseUsesBuildingGrid(t *testing.T) {
	response := office.EmptyLayoutResponse(models.OfficeRoomRef{
		Kind: models.OfficeRoomKindChannel,
		Ref:  "ch_1",
	})

	if response.RoomID != "channel:ch_1" {
		t.Fatalf("room id: got %q, want %q", response.RoomID, "channel:ch_1")
	}
	if response.Version != 0 || response.Status != "" {
		t.Fatalf("version/status: got %d/%q, want 0/empty", response.Version, response.Status)
	}
	if response.Grid.Width != 56 || response.Grid.Height != 36 || response.Grid.Tile != 1 {
		t.Fatalf("grid: got %+v, want 56x36 tile 1", response.Grid)
	}
	if response.Items == nil {
		t.Fatal("items should never be nil")
	}
	if len(response.Items) != 0 {
		t.Fatalf("items: got %d, want 0", len(response.Items))
	}
}
