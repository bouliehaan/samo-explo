package discovery

import (
	"encoding/json"
	"testing"
)

// The raw recommendation feed mixes tracks the user has heard with ones they
// have not, ranked by score. `latest_listened_at` is the field that separates
// them, and it is the whole difference between "exploration" and a playlist of
// songs you already know — so parsing it correctly is load-bearing.
const recommendationFixture = `{
  "payload": {
    "count": 6,
    "entity": "recording",
    "total_mbid_count": 1000,
    "user_name": "someone",
    "mbids": [
      {"latest_listened_at": "2026-05-06T19:33:42.000Z", "recording_mbid": "heard-1",  "score": 0.34},
      {"latest_listened_at": null,                       "recording_mbid": "unheard-1","score": 0.33},
      {"latest_listened_at": "2026-05-04T19:55:41.000Z", "recording_mbid": "heard-2",  "score": 0.32},
      {"latest_listened_at": null,                       "recording_mbid": "unheard-2","score": 0.31},
      {"latest_listened_at": null,                       "recording_mbid": "unheard-3","score": 0.30},
      {"latest_listened_at": null,                       "recording_mbid": "unheard-4","score": 0.29}
    ]
  }
}`

// A JSON null must stay distinguishable from a timestamp. As a bare time.Time
// it decoded to the zero value, which is indistinguishable from a date we
// failed to parse — and would have quietly classified heard tracks as unheard.
func TestLatestListenedAtDistinguishesNull(t *testing.T) {
	var reccs Recommendations
	if err := json.Unmarshal([]byte(recommendationFixture), &reccs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rows := reccs.Payload.Mbids
	if len(rows) != 6 {
		t.Fatalf("parsed %d rows, want 6", len(rows))
	}
	if rows[0].LatestListenedAt == nil {
		t.Fatal("a real timestamp must not parse as nil")
	}
	if rows[1].LatestListenedAt != nil {
		t.Fatal("a JSON null must parse as nil")
	}
}

// Selection keeps unheard tracks only, preserves the feed's score ranking, and
// stops at the requested size.
func TestSelectionKeepsRankedUnheard(t *testing.T) {
	var reccs Recommendations
	if err := json.Unmarshal([]byte(recommendationFixture), &reccs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	pick := func(want int) []string {
		var got []string
		for _, rec := range reccs.Payload.Mbids {
			if rec.LatestListenedAt != nil {
				continue
			}
			got = append(got, rec.RecordingMbid)
			if len(got) >= want {
				break
			}
		}
		return got
	}

	got := pick(3)
	expected := []string{"unheard-1", "unheard-2", "unheard-3"}
	if len(got) != len(expected) {
		t.Fatalf("selected %v, want %v", got, expected)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Fatalf("selected %v, want %v (ranking must be preserved)", got, expected)
		}
	}

	// Asking for more than exist yields everything unheard, never a heard one.
	all := pick(100)
	if len(all) != 4 {
		t.Fatalf("selected %d unheard, want 4", len(all))
	}
	for _, id := range all {
		if id == "heard-1" || id == "heard-2" {
			t.Fatalf("a previously heard recording leaked into the selection: %v", all)
		}
	}
}
