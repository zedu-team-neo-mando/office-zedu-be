package office

import (
	"errors"
	"fmt"
	"time"

	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/utility"
	"gorm.io/gorm"
)

// Layout errors that the controller maps onto status codes.
var (
	ErrLayoutNotFound  = errors.New("layout not found")
	ErrVersionConflict = errors.New("layout was changed by someone else")
	ErrInvalidLayout   = errors.New("layout is invalid")
)

const (
	// MaxItemsPerLayout keeps a single room's furniture payload sane.
	MaxItemsPerLayout = 200
	// MaxLayoutDimension bounds the building grid a layout may reference.
	MaxLayoutDimension = 512
)

// ValidateLayoutPayload rejects payloads that would corrupt the building.
func ValidateLayoutPayload(payload models.OfficeLayoutPayload) error {
	if payload.Grid.Width <= 0 || payload.Grid.Width > MaxLayoutDimension {
		return fmt.Errorf("%w: grid width must be between 1 and %d", ErrInvalidLayout, MaxLayoutDimension)
	}
	if payload.Grid.Height <= 0 || payload.Grid.Height > MaxLayoutDimension {
		return fmt.Errorf("%w: grid height must be between 1 and %d", ErrInvalidLayout, MaxLayoutDimension)
	}
	if payload.Grid.Tile <= 0 {
		return fmt.Errorf("%w: grid tile must be greater than zero", ErrInvalidLayout)
	}
	if len(payload.Items) > MaxItemsPerLayout {
		return fmt.Errorf("%w: a room cannot hold more than %d items", ErrInvalidLayout, MaxItemsPerLayout)
	}

	seen := map[string]bool{}
	for _, item := range payload.Items {
		if item.ID == "" {
			return fmt.Errorf("%w: every item needs an id", ErrInvalidLayout)
		}
		if item.CatalogID == "" {
			return fmt.Errorf("%w: every item needs a catalogue id", ErrInvalidLayout)
		}
		if seen[item.ID] {
			return fmt.Errorf("%w: duplicate item id %q", ErrInvalidLayout, item.ID)
		}
		seen[item.ID] = true

		switch item.Rotation {
		case 0, 90, 180, 270:
		default:
			return fmt.Errorf("%w: item %q has an invalid rotation %d", ErrInvalidLayout, item.ID, item.Rotation)
		}

		if item.X < 0 || item.Y < 0 ||
			item.X > float64(payload.Grid.Width) ||
			item.Y > float64(payload.Grid.Height) {
			return fmt.Errorf("%w: item %q sits outside the room", ErrInvalidLayout, item.ID)
		}
	}

	return nil
}

// GetPublishedLayout returns the live layout of a room, if there is one.
func (s *Service) GetPublishedLayout(orgID string, ref models.OfficeRoomRef) (*models.OfficeLayout, error) {
	var layout models.OfficeLayout
	err := s.Db.Postgresql.Where("organisation_id = ? AND room_kind = ? AND ref_id = ? AND status = ?",
		orgID, ref.Kind, ref.Ref, models.OfficeLayoutStatusPublished).
		First(&layout).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &layout, nil
}

// GetDraft returns the draft an admin is working on for a room.
func (s *Service) GetDraft(orgID string, ref models.OfficeRoomRef, ownerID string) (*models.OfficeLayout, error) {
	var layout models.OfficeLayout
	err := s.Db.Postgresql.Where("organisation_id = ? AND room_kind = ? AND ref_id = ? AND status = ? AND draft_owner_id = ?",
		orgID, ref.Kind, ref.Ref, models.OfficeLayoutStatusDraft, ownerID).
		First(&layout).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &layout, nil
}

// GetPersonalLayout returns the layout a user saved for their own office.
func (s *Service) GetPersonalLayout(orgID, ownerID string) (*models.OfficeLayout, error) {
	var layout models.OfficeLayout
	err := s.Db.Postgresql.Where("organisation_id = ? AND room_kind = ? AND ref_id = ? AND draft_owner_id = ? AND status = ?",
		orgID, models.OfficeRoomKindPersonal, ownerID, ownerID, models.OfficeLayoutStatusDraft).
		First(&layout).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &layout, nil
}

// GetAllPublishedLayouts returns all published layouts for an organisation in one query.
func (s *Service) GetAllPublishedLayouts(orgID string) ([]*models.OfficeLayout, error) {
	var layouts []*models.OfficeLayout
	err := s.Db.Postgresql.Where("organisation_id = ? AND status = ?", orgID, models.OfficeLayoutStatusPublished).
		Find(&layouts).Error
	if err != nil {
		return nil, err
	}
	return layouts, nil
}

// SaveDraft stores the admin's working copy of a shared room layout.
func (s *Service) SaveDraft(command models.OfficeLayoutCommand) (*models.OfficeLayout, error) {
	ref := command.Room
	ownerID := command.ActorID

	if err := ValidateLayoutPayload(command.Payload); err != nil {
		return nil, err
	}

	existing, err := s.GetDraft(command.OrganisationID, ref, ownerID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if existing != nil {
		existing.Layout = command.Payload
		existing.UpdatedBy = ownerID
		existing.UpdatedAt = now
		if err := s.Db.Postgresql.Save(existing).Error; err != nil {
			return nil, err
		}
		return existing, nil
	}

	layout := models.OfficeLayout{
		ID:             utility.GenerateUUID(),
		OrganisationID: command.OrganisationID,
		RoomKind:       ref.Kind,
		RefID:          ref.Ref,
		Version:        1,
		Status:         models.OfficeLayoutStatusDraft,
		DraftOwnerID:   &ownerID,
		Layout:         command.Payload,
		UpdatedBy:      ownerID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := s.Db.Postgresql.Create(&layout).Error; err != nil {
		return nil, err
	}
	return &layout, nil
}

// SavePersonalLayout autosaves the layout of a user's own office. Personal
// offices have no publish step: the owner's draft is their live layout.
func (s *Service) SavePersonalLayout(orgID, ownerID string, payload models.OfficeLayoutPayload) (*models.OfficeLayout, error) {
	if err := ValidateLayoutPayload(payload); err != nil {
		return nil, err
	}

	ref := models.OfficeRoomRef{Kind: models.OfficeRoomKindPersonal, Ref: ownerID}
	existing, err := s.GetPersonalLayout(orgID, ownerID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if existing != nil {
		existing.Layout = payload
		existing.UpdatedBy = ownerID
		existing.UpdatedAt = now
		if err := s.Db.Postgresql.Save(existing).Error; err != nil {
			return nil, err
		}
		return existing, nil
	}

	layout := models.OfficeLayout{
		ID:             utility.GenerateUUID(),
		OrganisationID: orgID,
		RoomKind:       ref.Kind,
		RefID:          ref.Ref,
		Version:        1,
		Status:         models.OfficeLayoutStatusDraft,
		DraftOwnerID:   &ownerID,
		Layout:         payload,
		UpdatedBy:      ownerID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := s.Db.Postgresql.Create(&layout).Error; err != nil {
		return nil, err
	}
	return &layout, nil
}

// Publish makes the room's draft the live layout and bumps the version. The
// expectedVersion guards against two admins publishing over each other.
func (s *Service) Publish(command models.OfficeLayoutCommand) (*models.OfficeLayout, error) {
	ref := command.Room

	current, err := s.GetPublishedLayout(command.OrganisationID, ref)
	if err != nil {
		return nil, err
	}

	var liveVersion int64
	if current != nil {
		liveVersion = current.Version
	}
	if command.Version > 0 && liveVersion != command.Version {
		return nil, ErrVersionConflict
	}

	payload := models.OfficeLayoutPayload{
		Grid:  models.OfficeGrid{Width: int(OfficeBuildingWidth), Height: int(OfficeBuildingHeight), Tile: OfficeTileSize},
		Items: []models.OfficeFurnitureItem{},
	}
	if draft, err := s.GetDraft(command.OrganisationID, ref, command.ActorID); err != nil {
		return nil, err
	} else if draft != nil {
		payload = draft.Layout
	}
	command.Payload = payload

	published, err := s.replacePublished(command)
	if err != nil {
		return nil, err
	}

	return published, nil
}

// Reset restores a shared room to its template and publishes it.
func (s *Service) Reset(orgID string, ref models.OfficeRoomRef, adminID string) (*models.OfficeLayout, error) {
	payload := models.OfficeLayoutPayload{
		Grid:  models.OfficeGrid{Width: int(OfficeBuildingWidth), Height: int(OfficeBuildingHeight), Tile: OfficeTileSize},
		Items: []models.OfficeFurnitureItem{},
	}

	if config, err := s.loadRoomConfig(orgID, ref); err != nil {
		return nil, err
	} else if config != nil && len(config.Template.Items) > 0 {
		payload = config.Template
	}

	return s.replacePublished(models.OfficeLayoutCommand{
		OrganisationID: orgID,
		Room:           ref,
		ActorID:        adminID,
		Payload:        payload,
	})
}

// replacePublished supersedes the live layout and writes the next version.
func (s *Service) replacePublished(command models.OfficeLayoutCommand) (*models.OfficeLayout, error) {
	orgID := command.OrganisationID
	ref := command.Room
	editorID := command.ActorID
	payload := command.Payload

	now := time.Now().UTC()
	var nextVersion int64 = 1

	err := s.Db.Postgresql.Transaction(func(tx *gorm.DB) error {
		var current models.OfficeLayout
		err := tx.Where("organisation_id = ? AND room_kind = ? AND ref_id = ? AND status = ?",
			orgID, ref.Kind, ref.Ref, models.OfficeLayoutStatusPublished).
			First(&current).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		if err == nil {
			nextVersion = current.Version + 1
			if err := tx.Model(&current).Update("status", models.OfficeLayoutStatusSuperseded).Error; err != nil {
				return err
			}
		}

		layout := models.OfficeLayout{
			ID:             utility.GenerateUUID(),
			OrganisationID: orgID,
			RoomKind:       ref.Kind,
			RefID:          ref.Ref,
			Version:        nextVersion,
			Status:         models.OfficeLayoutStatusPublished,
			Layout:         payload,
			UpdatedBy:      editorID,
			PublishedAt:    &now,
			CreatedAt:      now,
			UpdatedAt:      now,
		}

		return tx.Create(&layout).Error
	})
	if err != nil {
		return nil, err
	}

	published, err := s.GetPublishedLayout(orgID, ref)
	if err != nil {
		return nil, err
	}
	return published, nil
}

// EmptyLayoutResponse is the layout an editor receives before a room has any
// saved layout. Both the layout and draft endpoints return it, so the two
// never disagree about the default grid.
func EmptyLayoutResponse(ref models.OfficeRoomRef) models.OfficeLayoutResponse {
	return models.OfficeLayoutResponse{
		RoomID:  EncodeRoomID(ref.Kind, ref.Ref),
		Version: 0,
		Status:  "",
		Grid:    models.OfficeGrid{Width: int(OfficeBuildingWidth), Height: int(OfficeBuildingHeight), Tile: OfficeTileSize},
		Items:   []models.OfficeFurnitureItem{},
	}
}

// ToResponse converts a stored layout into the API payload.
func ToResponse(layout *models.OfficeLayout) models.OfficeLayoutResponse {
	if layout == nil {
		return models.OfficeLayoutResponse{}
	}

	items := layout.Layout.Items
	if items == nil {
		items = []models.OfficeFurnitureItem{}
	}

	return models.OfficeLayoutResponse{
		RoomID:    EncodeRoomID(layout.RoomKind, layout.RefID),
		Version:   layout.Version,
		Status:    layout.Status,
		Grid:      layout.Layout.Grid,
		Items:     items,
		UpdatedAt: layout.UpdatedAt,
		UpdatedBy: layout.UpdatedBy,
	}
}
