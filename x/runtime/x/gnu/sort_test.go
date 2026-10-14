// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gnu

import (
	"testing"
)

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		// Basic version comparisons
		{"1.0", "2.0", -1},
		{"2.0", "1.0", 1},
		{"1.0", "1.0", 0},

		// Multi-part versions
		{"1.0.0", "1.0.1", -1},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.0", 0},
		{"1.2.3", "1.2.4", -1},
		{"1.2.10", "1.2.9", 1},

		// Numeric comparison (not lexicographic)
		{"1.10", "1.9", 1},
		{"1.2", "1.10", -1},
		{"10", "9", 1},
		{"2", "10", -1},

		// Leading zeros
		{"1.01", "1.1", 0},
		{"1.001", "1.1", 0},
		{"01", "1", 0},
		{"001", "01", 0},

		// Empty strings
		{"", "", 0},
		{"1", "", 1},
		{"", "1", -1},

		// Tilde (sorts before everything, including empty)
		{"1.0~rc1", "1.0", -1},
		{"1.0~", "1.0", -1},
		{"1.0~alpha", "1.0~beta", -1},
		{"~", "", -1},

		// Letters vs numbers
		{"a", "1", 1},
		{"1a", "1b", -1},
		{"1.0a", "1.0b", -1},
		{"1.0a", "1.0", 1},

		// Complex version strings
		{"1.0alpha", "1.0beta", -1},
		{"1.0alpha1", "1.0alpha2", -1},
		{"1.0.0-rc1", "1.0.0-rc2", -1},
		{"1.0.0-rc10", "1.0.0-rc9", 1},

		// Real-world examples
		{"2.6.32", "2.6.32.1", -1},
		{"3.0", "2.6.39", 1},
		{"0.9.9", "1.0.0", -1},

		// With prefixes
		{"v1.0.0", "v1.0.1", -1},
		{"v2.0", "v10.0", -1},
		{"release-1.0", "release-2.0", -1},

		// Edge cases with special characters
		{"1.0.0", "1.0.0.0", -1},
		{"1-2", "1.2", -1}, // '-' (45+256=301) vs '.' (46+256=302)
		{"1_2", "1.2", 1},  // '_' (95+256=351) vs '.' (46+256=302)

		// Debian-style versions
		{"1.0+git20200101", "1.0+git20200102", -1},
		{"1.0~git20200101", "1.0", -1},
	}

	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			got := Compare(tt.a, tt.b)
			// Normalize to -1, 0, 1
			if got < 0 {
				got = -1
			} else if got > 0 {
				got = 1
			}
			if got != tt.want {
				t.Errorf("Compare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestCompareSymmetry(t *testing.T) {
	pairs := [][2]string{
		{"1.0", "2.0"},
		{"1.10", "1.9"},
		{"1.0~rc1", "1.0"},
		{"a", "b"},
		{"1.0alpha", "1.0beta"},
	}

	for _, pair := range pairs {
		a, b := pair[0], pair[1]
		ab := Compare(a, b)
		ba := Compare(b, a)

		// Normalize
		if ab < 0 {
			ab = -1
		} else if ab > 0 {
			ab = 1
		}
		if ba < 0 {
			ba = -1
		} else if ba > 0 {
			ba = 1
		}

		if ab != -ba {
			t.Errorf("Symmetry violated: Compare(%q, %q)=%d, Compare(%q, %q)=%d", a, b, ab, b, a, ba)
		}
	}
}

func TestCompareReflexive(t *testing.T) {
	versions := []string{
		"",
		"0",
		"1",
		"1.0",
		"1.0.0",
		"1.0~rc1",
		"1.0alpha",
		"v2.0.0",
	}

	for _, v := range versions {
		if got := Compare(v, v); got != 0 {
			t.Errorf("Compare(%q, %q) = %d, want 0", v, v, got)
		}
	}
}

func TestOrder(t *testing.T) {
	tests := []struct {
		c    byte
		want int
	}{
		{'0', 0},
		{'9', 0},
		{'a', int('a')},
		{'z', int('z')},
		{'A', int('A')},
		{'Z', int('Z')},
		{'~', -1},
		{0, 0},
		{'.', int('.') + 256},
		{'-', int('-') + 256},
		{'_', int('_') + 256},
	}

	for _, tt := range tests {
		t.Run(string(tt.c), func(t *testing.T) {
			if got := order(tt.c); got != tt.want {
				t.Errorf("order(%q) = %d, want %d", tt.c, got, tt.want)
			}
		})
	}
}

func TestIsDigit(t *testing.T) {
	for c := byte(0); c < 128; c++ {
		want := c >= '0' && c <= '9'
		if got := isDigit(c); got != want {
			t.Errorf("isDigit(%q) = %v, want %v", c, got, want)
		}
	}
}

func TestIsAlpha(t *testing.T) {
	for c := byte(0); c < 128; c++ {
		want := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if got := isAlpha(c); got != want {
			t.Errorf("isAlpha(%q) = %v, want %v", c, got, want)
		}
	}
}
