package domain

import "testing"

func TestRangeWindow(t *testing.T) {
	const now = 1_791_460_812_345 // 2026-10-08T12:00:12.345Z
	cases := []struct {
		r        TimeRange
		from, to int64
		count    int
	}{
		{Range15m, 1_791_459_930_000, 1_791_460_830_000, 30},
		{Range1h, 1_791_457_320_000, 1_791_460_920_000, 30},
		{Range24h, 1_791_378_000_000, 1_791_464_400_000, 24},
		{Range7d, 1_790_877_600_000, 1_791_482_400_000, 28},
	}
	for _, c := range cases {
		from, to, count := RangeWindow(c.r, now)
		if from != c.from || to != c.to || count != c.count {
			t.Errorf("%s: %d %d %d, want %d %d %d", c.r, from, to, count, c.from, c.to, c.count)
		}
		spec, _ := c.r.Spec()
		if now < to-spec.BinMs || now >= to {
			t.Errorf("%s: the last bucket does not hold now", c.r)
		}
	}
	// On a bucket boundary, now opens the last bucket.
	from, to, _ := RangeWindow(Range1h, 1_791_460_800_000)
	if to != 1_791_460_920_000 || from != 1_791_457_320_000 {
		t.Errorf("boundary: %d %d", from, to)
	}
	if TimeRange("2d").Valid() || !Range7d.Valid() {
		t.Error("Valid")
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
	long := "a"
	for len(long) < 128 {
		long += "b"
	}
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
