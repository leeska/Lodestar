package model

import (
	"math"
	"testing"
)

func TestReferenceConversionRoundsExactlyAndChecksOverflow(t *testing.T) {
	for _, test := range []struct {
		amount   int64
		from, to string
		want     int64
	}{{1, "CNY", "JPY", 21}, {-1, "CNY", "JPY", -21}, {100, "USD", "CNY", 710}, {8, "CNY", "CNY", 8}} {
		got, ok := ConvertReferenceMinor(test.amount, test.from, test.to)
		if !ok || got != test.want {
			t.Fatal(test, got, ok)
		}
	}
	if _, ok := ConvertReferenceMinor(math.MaxInt64, "GBP", "KRW"); ok {
		t.Fatal("overflowed conversion accepted")
	}
	if _, ok := ConvertReferenceMinor(1, "unknown", "CNY"); ok {
		t.Fatal("unknown currency accepted")
	}
	got, ok := ConvertReferenceTotals(map[string]int64{"KRW": 300}, "CNY")
	if !ok || got != 2 {
		t.Fatal(got, ok)
	}
}
