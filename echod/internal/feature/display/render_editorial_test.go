//go:build !dot && !spot

package display

import (
	"testing"
	"time"
)

func TestAlmanacMonthAlignment(t *testing.T) {
	cases := []struct {
		name  string
		year  int
		month time.Month
		first int
		days  int
		rows  int
	}{
		{"non-leap century", 1900, time.February, 3, 28, 5},
		{"leap century", 2000, time.February, 1, 29, 5},
		{"December year end", 2023, time.December, 4, 31, 5},
		{"January year start", 2024, time.January, 0, 31, 5},
		{"four complete weeks", 2021, time.February, 0, 28, 4},
		{"Sunday start six rows", 2026, time.March, 6, 31, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(tc.year, tc.month, tc.days, 23, 59, 0, 0, time.UTC)
			month := almanacMonthFor(now)
			if month.year != tc.year || month.month != tc.month || month.first != tc.first || month.days != tc.days || month.rows != tc.rows || month.today != tc.days {
				t.Fatalf("month = %+v; want year=%d month=%d first=%d days=%d rows=%d today=%d", month, tc.year, tc.month, tc.first, tc.days, tc.rows, tc.days)
			}
			next := 1
			for row := range month.rows {
				for column := range 7 {
					day := month.dayAt(row, column)
					index := row*7 + column
					if index < tc.first || index >= tc.first+tc.days {
						if day != 0 {
							t.Errorf("padding cell (%d,%d) contains day %d", row, column, day)
						}
						continue
					}
					if day != next {
						t.Fatalf("cell (%d,%d) = %d; want consecutive day %d", row, column, day, next)
					}
					weekday := time.Date(tc.year, tc.month, day, 12, 0, 0, 0, now.Location()).Weekday()
					if (int(weekday)+6)%7 != column {
						t.Errorf("day %d is %v, but appears in Monday-first column %d", day, weekday, column)
					}
					next++
				}
			}
			if next != tc.days+1 {
				t.Errorf("grid ends after day %d; want %d", next-1, tc.days)
			}
			for _, cell := range [][2]int{{-1, 0}, {month.rows, 0}, {0, -1}, {0, 7}} {
				if day := month.dayAt(cell[0], cell[1]); day != 0 {
					t.Errorf("outside cell %v contains day %d", cell, day)
				}
			}
		})
	}
}

func TestAlmanacUsesSceneLocalDate(t *testing.T) {
	instant := time.Date(2025, time.December, 31, 23, 30, 0, 0, time.UTC)
	cases := []struct {
		offset int
		year   int
		month  time.Month
		day    int
		column int
	}{
		{14 * 3600, 2026, time.January, 1, 3},
		{-12 * 3600, 2025, time.December, 31, 2},
	}
	for _, tc := range cases {
		now := instant.In(time.FixedZone("scene", tc.offset))
		month := almanacMonthFor(now)
		if month.year != tc.year || month.month != tc.month || month.today != tc.day {
			t.Errorf("offset %d: month = %+v; want local date %04d-%02d-%02d", tc.offset, month, tc.year, tc.month, tc.day)
		}
		index := month.first + month.today - 1
		if index%7 != tc.column || month.dayAt(index/7, index%7) != tc.day {
			t.Errorf("offset %d: today's grid cell (%d,%d) does not match local day %d in column %d", tc.offset, index/7, index%7, tc.day, tc.column)
		}
	}
}
