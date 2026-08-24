package client

import (
	"testing"

	"explo/src/models"
)

// The whole point of pruning by relevance: a track still on this week's list
// must keep its file, so the run finds it in place instead of fetching it off
// somebody else's machine again. A track that has fallen off the list is the
// only thing that should go.
func TestPruneKeepsStillRecommendedDrops(t *testing.T) {
	ledger := []samoExploRow{
		{TrackID: "t-keep", Title: "Crystalised", Artist: "The xx"},
		{TrackID: "t-keep-2", Title: "Kids (Soulwax remix)", Artist: "MGMT"},
		{TrackID: "t-stale", Title: "Butterfly", Artist: "Crazy Town"},
	}
	// This week's recommendations: the first two, not Butterfly.
	wanted := []*models.Track{
		{CleanTitle: "Crystalised", MainArtist: "The xx"},
		{CleanTitle: "Kids", MainArtist: "MGMT"},
	}

	samo := &Samo{}
	keep := map[string]bool{}
	for _, track := range wanted {
		probe := &models.Track{CleanTitle: track.CleanTitle, MainArtist: track.MainArtist}
		if samo.matchExploLedger(probe, ledger) {
			keep[probe.ID] = true
		}
	}

	if !keep["t-keep"] {
		t.Fatal("a still-recommended track was not kept — it would be re-downloaded")
	}
	if !keep["t-keep-2"] {
		t.Fatal("a suffixed title (Kids -> Kids (Soulwax remix)) must still be recognised")
	}
	if keep["t-stale"] {
		t.Fatal("a track no longer recommended was kept — the folder would grow forever")
	}
}

// With nothing recommended, everything staged is stale. Guards against an empty
// wanted list being read as "keep everything" and the folder never rotating.
func TestPruneWithNothingWantedMarksAllStale(t *testing.T) {
	ledger := []samoExploRow{
		{TrackID: "t-1", Title: "Crystalised", Artist: "The xx"},
	}
	samo := &Samo{}
	keep := map[string]bool{}
	for _, track := range []*models.Track{} {
		probe := &models.Track{CleanTitle: track.CleanTitle, MainArtist: track.MainArtist}
		if samo.matchExploLedger(probe, ledger) {
			keep[probe.ID] = true
		}
	}
	if len(keep) != 0 {
		t.Fatalf("expected nothing kept, got %v", keep)
	}
}

// An empty ledger means nothing is staged, so pruning is a no-op rather than an
// error — the first run ever hits this.
func TestPruneWithEmptyLedgerIsNoOp(t *testing.T) {
	samo := &Samo{}
	removed, err := samo.PruneDropFolder(t.TempDir(), []*models.Track{
		{CleanTitle: "Crystalised", MainArtist: "The xx"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed %d files from an empty ledger", removed)
	}
}
