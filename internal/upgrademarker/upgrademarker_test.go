package upgrademarker_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/upgrademarker"
)

func sample() upgrademarker.Marker {
	return upgrademarker.Marker{
		At:   time.Date(2026, 11, 10, 8, 30, 15, 999, time.FixedZone("x", 3600)),
		From: upgrademarker.Build{Version: "v1.2.3-4-gabc", Commit: "aaaaaaaaaaaa", Schema: 16},
		To:   upgrademarker.Build{Version: "v1.2.3-9-gdef", Commit: "bbbbbbbbbbbb", Schema: 17},
	}
}

func TestRoundTrip(t *testing.T) {
	data, err := upgrademarker.Encode(sample())
	if err != nil {
		t.Fatal(err)
	}
	got, err := upgrademarker.Decode(data)
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	want := sample()
	want.Format = upgrademarker.Format
	want.At = time.Date(2026, 11, 10, 7, 30, 15, 0, time.UTC)
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestDecodeIsStrict(t *testing.T) {
	good, err := upgrademarker.Encode(sample())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"empty":          "",
		"unknown field":  strings.Replace(string(good), `"format": 1,`, `"format": 1, "extra": 1,`, 1),
		"trailing data":  string(good) + "{}",
		"other format":   strings.Replace(string(good), `"format": 1`, `"format": 2`, 1),
		"long commit":    strings.Replace(string(good), "bbbbbbbbbbbb", strings.Repeat("b", 41), 1),
		"short commit":   strings.Replace(string(good), "bbbbbbbbbbbb", "bbbbbb", 1),
		"upper commit":   strings.Replace(string(good), "bbbbbbbbbbbb", "BBBBBBBBBBBB", 1),
		"bad version":    strings.Replace(string(good), "v1.2.3-9-gdef", "v1 2", 1),
		"zero schema":    strings.Replace(string(good), `"schema": 17`, `"schema": 0`, 1),
		"too large":      strings.Repeat(" ", upgrademarker.MaxSize+1),
		"not an object":  "[]",
		"missing time":   strings.Replace(string(good), `"at": "2026-11-10T07:30:15Z"`, `"at": "0001-01-01T00:00:00Z"`, 1),
		"control in tag": strings.Replace(string(good), "v1.2.3-9-gdef", "v1\\u001b[2J", 1),
	} {
		if _, err := upgrademarker.Decode([]byte(data)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSameCommit(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"bbbbbbbbbbbb", "bbbbbbbbbbbb", true},
		{"bbbbbbbbbbbb", "bbbbbbbbbbbb0123456789abcdef0123456789ab", true},
		{"bbbbbbb0123456789abcdef0123456789abcdef0", "bbbbbbb", true},
		{"BBBBBBBBBBBB", "bbbbbbbbbbbb", true},
		{"bbbbbbbbbbbb", "bbbbbbbbbbbc", false},
		{"bbbbbb", "bbbbbb", false},
		{"", "", false},
	} {
		if got := upgrademarker.SameCommit(tc.a, tc.b); got != tc.want {
			t.Errorf("SameCommit(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
}
