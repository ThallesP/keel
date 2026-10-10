package domain

import (
	"strings"
	"testing"
)

func TestRangeWindow(t *testing.T) {
	const now = 1_791_460_812_345
	cases := []struct {
		r        TimeRange
		from, to int64
	}{
		{Range15m, 1_791_459_930_000, 1_791_460_830_000},
		{Range1h, 1_791_457_320_000, 1_791_460_920_000},
		{Range24h, 1_791_378_000_000, 1_791_464_400_000},
		{Range7d, 1_790_877_600_000, 1_791_482_400_000},
	}
	for _, c := range cases {
		spec, _ := c.r.Spec()
		if from, to := spec.Window(now); from != c.from || to != c.to {
			t.Errorf("%s: %d %d, want %d %d", c.r, from, to, c.from, c.to)
		}
	}
	spec, _ := Range1h.Spec()
	if from, to := spec.Window(1_791_460_800_000); to != 1_791_460_920_000 || from != 1_791_457_320_000 {
		t.Errorf("boundary: %d %d", from, to)
	}
	if _, ok := TimeRange("2d").Spec(); ok {
		t.Error("2d is not a range")
	}
}

func TestValidDataset(t *testing.T) {
	for name, want := range map[string]bool{
		"keel-logs": true, "a": true, "A.b_c-9": true, "-x": false, ".x": false, "": false, "a b": false,
		"a/b": false, "é": false, "a\n": false, string(make([]byte, 129)): false,
	} {
		if got := ValidDataset(name); got != want {
			t.Errorf("ValidDataset(%q) = %v", name, got)
		}
	}
	long := "a" + strings.Repeat("b", 127)
	if !ValidDataset(long) || ValidDataset(long+"c") {
		t.Error("128-character limit")
	}
}

func TestTokenHintsAndMasks(t *testing.T) {
	if got := TokenHint("xaat-1234abcd"); got != "…abcd" {
		t.Errorf("TokenHint = %q", got)
	}
	if got := TokenHint("ab"); got != "…ab" {
		t.Errorf("TokenHint short = %q", got)
	}
	if got := MaskOTLPKey("keel_otlp_abcdefghWXYZ"); got != "keel_otlp_…WXYZ" {
		t.Errorf("MaskOTLPKey = %q", got)
	}
	if got := MaskOTLPKey(""); got != "keel_otlp_…" {
		t.Errorf("MaskOTLPKey empty = %q", got)
	}
}
