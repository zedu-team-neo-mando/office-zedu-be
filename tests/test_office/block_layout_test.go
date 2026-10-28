package test_office

import (
	"math"
	"testing"

	"github.com/hngprojects/telex_be/internal/models"
	"github.com/hngprojects/telex_be/services/office"
)

func TestOfficeBlocksKeepSharedWallsAndInternalCorridors(t *testing.T) {
	for _, c := range []struct {
		kind                  string
		columns, split, count int
		width, depth          float64
	}{
		{models.OfficeRoomKindPersonal, 10, 5, 40, 5, 5.4},
		{models.OfficeRoomKindChannel, 7, 3, 14, 7.3, 7.4},
	} {
		for i := 0; i < c.count; i++ {
			b := office.DefaultRoomBounds(c.kind, "", i)
			if b.W != c.width || b.H != c.depth {
				t.Fatalf("%s room %d changed size: %+v", c.kind, i, b)
			}
			if i%c.columns > 0 {
				a := office.DefaultRoomBounds(c.kind, "", i-1)
				want := 0.0
				if i%c.columns == c.split {
					want = 2
				}
				if math.Abs(b.X-a.X-a.W-want) > 1e-8 {
					t.Fatalf("%s room %d side gap: %+v %+v", c.kind, i, a, b)
				}
			}
			if i >= c.columns {
				a := office.DefaultRoomBounds(c.kind, "", i-c.columns)
				if math.Abs(b.Y-a.Y-a.H-2) > 1e-8 {
					t.Fatalf("%s room %d row corridor changed", c.kind, i)
				}
			}
		}
	}
}

func TestMemberAreaPassagesStillConnectToOfficeRows(t *testing.T) {
	last := office.DefaultRoomBounds(models.OfficeRoomKindPersonal, "", 39)
	area := office.DefaultRoomBounds(models.OfficeRoomKindArea, "", 0)
	if math.Abs(area.Y-last.Y-last.H-2) > 1e-8 {
		t.Fatal("missing cross-corridor before shared areas")
	}
	for i := 1; i < 3; i++ {
		a := office.DefaultRoomBounds(models.OfficeRoomKindArea, "", i-1)
		b := office.DefaultRoomBounds(models.OfficeRoomKindArea, "", i)
		if math.Abs(b.X-a.X-a.W-2) > 1e-8 {
			t.Fatal("area corridor changed")
		}
	}
}

func TestThreeChannelRoomsAreEqualAndReachBothEnds(t *testing.T) {
	first := office.ChannelRoomBounds(0, 3)
	middle := office.ChannelRoomBounds(1, 3)
	last := office.ChannelRoomBounds(2, 3)
	if first.W != middle.W || middle.W != last.W || first.W <= 7.3 {
		t.Fatal("rooms should be equally enlarged")
	}
	if first.X != 0.8 || math.Abs(last.X+last.W-31) > 1e-8 {
		t.Fatal("rooms must reach both ends of the frontage")
	}
	if math.Abs(middle.X+middle.W/2-(0.8+31)/2) > 1e-8 {
		t.Fatal("middle room must be centred")
	}
	if math.Abs(middle.X-first.X-first.W-2) > 1e-8 || math.Abs(last.X-middle.X-middle.W-2) > 1e-8 {
		t.Fatal("both corridors must remain two tiles wide")
	}
	for _, count := range []int{1, 2, 4, 7, 14} {
		if office.ChannelRoomBounds(0, count) != office.DefaultRoomBounds(models.OfficeRoomKindChannel, "", 0) {
			t.Fatal("unrelated layouts changed")
		}
	}
}

func TestMeetingPairSharesThinWallWithoutMovingSecondRoom(t *testing.T) {
	first := office.DefaultRoomBounds(models.OfficeRoomKindMeeting, "", 0)
	second := office.DefaultRoomBounds(models.OfficeRoomKindMeeting, "", 1)
	if second.X != 25 || second.Y != 1 {
		t.Fatal("Meeting 2 moved")
	}
	if first.W != second.W || first.H != second.H || first.W <= 6 {
		t.Fatal("meeting rooms must be equally enlarged")
	}
	if math.Abs(first.X+first.W-second.X) > 1e-8 {
		t.Fatal("meeting rooms must share one wall")
	}
	upper := office.DefaultRoomBounds(models.OfficeRoomKindFocus, "", 1)
	lower := office.DefaultRoomBounds(models.OfficeRoomKindFocus, "", 3)
	gaps := []float64{upper.Y - second.Y - second.H, lower.Y - upper.Y - upper.H, office.ChannelRoomBounds(0, 3).Y - lower.Y - lower.H}
	for i, gap := range gaps {
		want := 2.0
		if i == 2 {
			want = 3
		}
		if math.Abs(gap-want) > 1e-8 {
			t.Fatal("incorrect corridor spacing")
		}
	}
	if upper.W != 4 || upper.H != 4 || lower.W != 4 || lower.H != 4 {
		t.Fatal("focus rooms must retain their size")
	}
	for row := 0; row < 2; row++ {
		left := office.DefaultRoomBounds(models.OfficeRoomKindFocus, "", row*2)
		right := office.DefaultRoomBounds(models.OfficeRoomKindFocus, "", row*2+1)
		if left.Y != right.Y || left.H != right.H || math.Abs(right.X-left.X-left.W-2) > 1e-8 {
			t.Fatal("focus pair is not aligned around a two-tile corridor")
		}
	}
}

func TestReceptionToMeetingCorridorIsTwoTilesWithoutMovingMeetingRooms(t *testing.T) {
	reception := office.DefaultRoomBounds(models.OfficeRoomKindReception, "", 0)
	first := office.DefaultRoomBounds(models.OfficeRoomKindMeeting, "1", 0)
	second := office.DefaultRoomBounds(models.OfficeRoomKindMeeting, "2", 1)
	if first.X-reception.X-reception.W != 2 {
		t.Fatal("reception corridor must be two tiles wide")
	}
	if first.X != 18 || second.X != 25 || first.W != 7 || second.W != 7 {
		t.Fatal("approved meeting layout changed")
	}
	if reception.X != 9 || reception.Y != 1 || reception.H != 6 {
		t.Fatal("reception moved instead of trimming its right edge")
	}
}
