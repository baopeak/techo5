//go:build !dot && !spot

package display

import (
	"math"
	"testing"
	"time"
)

func TestInstrumentTimeCycleBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name    string
		h, m    int
		h24     bool
		digits  [4]int
		hours   float64
		minutes float64
	}{
		{"midnight12", 0, 0, false, [4]int{1, 2, 0, 0}, 0, 0},
		{"noon12", 12, 0, false, [4]int{1, 2, 0, 0}, 0, 0},
		{"midnight24", 0, 0, true, [4]int{0, 0, 0, 0}, 0, 0},
		{"noon24", 12, 0, true, [4]int{1, 2, 0, 0}, 0.5, 0},
		{"leadingBlank12", 9, 5, false, [4]int{-1, 9, 0, 5}, (9.0 + 5.0/60) / 12, 5.0 / 60},
		{"leadingZero24", 9, 5, true, [4]int{0, 9, 0, 5}, (9.0 + 5.0/60) / 24, 5.0 / 60},
		{"lastMinute12", 23, 59, false, [4]int{1, 1, 5, 9}, (11.0 + 59.0/60) / 12, 59.0 / 60},
		{"lastMinute24", 23, 59, true, [4]int{2, 3, 5, 9}, (23.0 + 59.0/60) / 24, 59.0 / 60},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Seconds must not animate a minute-stepped instrument.
			for _, second := range []int{0, 59} {
				at := time.Date(2026, 10, 20, tt.h, tt.m, second, 0, time.FixedZone("local", 7*60*60))
				digits, hours, minutes := instrumentTime(at, tt.h24)
				if digits != tt.digits || math.Abs(hours-tt.hours) > 1e-12 || math.Abs(minutes-tt.minutes) > 1e-12 {
					t.Errorf("second %d: got %v %.12f %.12f; want %v %.12f %.12f", second, digits, hours, minutes, tt.digits, tt.hours, tt.minutes)
				}
			}
		})
	}
}

func TestMosaicHandsTraceDigitOutlines(t *testing.T) {
	// Edges between the six clock centers: top, upper-left, upper-right,
	// middle, lower-left, lower-right, bottom. This tests the assembled glyph,
	// rather than merely echoing individual clock-hand pairs from the table.
	edges := [7][2]int{{0, 1}, {0, 2}, {1, 3}, {2, 3}, {2, 4}, {3, 5}, {4, 5}}
	want := [10]uint8{
		0b1110111, // 0: closed perimeter, no middle stroke
		0b0100100, // 1: right column only
		0b1011101, // 2
		0b1101101, // 3
		0b0101110, // 4
		0b1101011, // 5
		0b1111011, // 6
		0b0100101, // 7
		0b1111111, // 8
		0b1101111, // 9
	}
	var seen [10]uint8
	for digit, clocks := range mosaicHandTable {
		var coverage [7]uint8
		for cell, hands := range clocks {
			// Identical hands are a single straight arm, not two connections.
			for handIndex, hand := range hands {
				if hand == mosaicPark || handIndex == 1 && hand == hands[0] {
					continue
				}
				neighbor := -1
				switch hand {
				case mosaicNorth:
					if cell >= 2 {
						neighbor = cell - 2
					}
				case mosaicEast:
					if cell%2 == 0 {
						neighbor = cell + 1
					}
				case mosaicSouth:
					if cell < 4 {
						neighbor = cell + 2
					}
				case mosaicWest:
					if cell%2 == 1 {
						neighbor = cell - 1
					}
				}
				if neighbor < 0 {
					t.Errorf("digit %d clock %d: hand %d points outside the digit", digit, cell, hand)
					continue
				}
				for edge, ends := range edges {
					if ends == [2]int{cell, neighbor} || ends == [2]int{neighbor, cell} {
						coverage[edge]++
						seen[digit] |= 1 << edge
					}
				}
			}
		}
		if seen[digit] != want[digit] {
			t.Errorf("digit %d: outlined edges %07b, want %07b", digit, seen[digit], want[digit])
		}
		if digit == 0 || digit == 2 {
			// These unbranched outlines must meet in both directions. Branching
			// digits intentionally use one-sided arms at three-way junctions.
			for edge, count := range coverage {
				if want[digit]&(1<<edge) != 0 && count != 2 {
					t.Errorf("digit %d edge %d: %d arms, want reciprocal connection", digit, edge, count)
				}
			}
		}
		for previous := range digit {
			if seen[previous] == seen[digit] {
				t.Errorf("digits %d and %d have indistinguishable outlines", previous, digit)
			}
		}
	}
}
