package discovery

import (
	"fmt"
	"strings"
	"testing"
)

// The weekly run died on a 502 from metadata/recording/ that looked like a
// ListenBrainz outage and was not one: 200 recommendation MBIDs went into a
// single 7486-byte URL, and their proxy rejects a request line over ~4KB.
// Measured against the live API, 101 MBIDs answered 200 and 110 answered 502.
const mbidRequestLimit = 4096

func syntheticMbids(n int) []string {
	mbids := make([]string, n)
	for i := range mbids {
		// Same 36-byte shape as a real MBID, which is what drives URL length.
		mbids[i] = fmt.Sprintf("%08d-0000-0000-0000-%012d", i, i)
	}
	return mbids
}

// Every MBID must survive batching exactly once and in order — a dropped batch
// would silently shrink the week rather than fail it.
func TestBatchMbidsCoversInputExactlyOnce(t *testing.T) {
	for _, total := range []int{0, 1, 49, 50, 51, 200, 1000} {
		mbids := syntheticMbids(total)
		batches := batchMbids(mbids, mbidBatchSize)

		var flat []string
		for _, batch := range batches {
			if len(batch) > mbidBatchSize {
				t.Fatalf("total %d: batch of %d exceeds %d", total, len(batch), mbidBatchSize)
			}
			if len(batch) == 0 {
				t.Fatalf("total %d: empty batch", total)
			}
			flat = append(flat, batch...)
		}

		if len(flat) != total {
			t.Fatalf("total %d: batches held %d mbids", total, len(flat))
		}
		for i, mbid := range flat {
			if mbid != mbids[i] {
				t.Fatalf("total %d: position %d is %q, want %q", total, i, mbid, mbids[i])
			}
		}
	}
}

// The regression guard: no batch may build a URL long enough to earn a 502.
// The longest `inc` of the two call sites is the one to measure against.
func TestBatchedRequestURLStaysUnderProxyLimit(t *testing.T) {
	const widestInc = "release+artist+tag+release_group+recording"

	for _, batch := range batchMbids(syntheticMbids(1000), mbidBatchSize) {
		url := fmt.Sprintf("https://api.listenbrainz.org/1/metadata/recording/?recording_mbids=%s&inc=%s",
			strings.Join(batch, ","), widestInc)

		if len(url) >= mbidRequestLimit {
			t.Fatalf("a %d-mbid batch builds a %d-byte URL, at or over the %d-byte limit",
				len(batch), len(url), mbidRequestLimit)
		}
	}
}

// A zero or negative size must not spin forever building empty batches.
func TestBatchMbidsRejectsNonPositiveSize(t *testing.T) {
	batches := batchMbids(syntheticMbids(3), 0)
	if len(batches) != 3 {
		t.Fatalf("size 0 produced %d batches, want 3 single-mbid batches", len(batches))
	}
}
