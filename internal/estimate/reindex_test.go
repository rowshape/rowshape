package estimate

import (
	"math"
	"testing"
)

// reindexBytesPerMs states ~50 MB/s. Written with an integer literal divisor the
// constant expression truncates (52428 instead of 52428.8), so the constant
// quietly is not the number its own comment claims.
func TestReindexBytesPerMsIsNotTruncated(t *testing.T) {
	const want = 50 * 1024 * 1024 / 1000.0
	if math.Abs(reindexBytesPerMs-want) > 1e-9 {
		t.Errorf("reindexBytesPerMs = %v, want %v — integer constant division truncated it", reindexBytesPerMs, want)
	}
	if reindexBytesPerMs == math.Trunc(reindexBytesPerMs) {
		t.Errorf("reindexBytesPerMs = %v is a whole number; 50MiB/1000 is 52428.8, so this is the truncated value", reindexBytesPerMs)
	}
}
