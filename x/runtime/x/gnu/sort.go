package gnu

/* Compare file names containing version numbers.

   Copyright (C) 1995 Ian Jackson <iwj10@cus.cam.ac.uk>
   Copyright (C) 2001 Anthony Towns <aj@azure.humbug.org.au>
   Copyright (C) 2008-2025 Free Software Foundation, Inc.

   This file is free software: you can redistribute it and/or modify
   it under the terms of the GNU Lesser General Public License as
   published by the Free Software Foundation, either version 3 of the
   License, or (at your option) any later version.

   This file is distributed in the hope that it will be useful,
   but WITHOUT ANY WARRANTY; without even the implied warranty of
   MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
   GNU Lesser General Public License for more details.

   You should have received a copy of the GNU Lesser General Public License
   along with this program.  If not, see <https://www.gnu.org/licenses/>.  */

// Compare compares two version strings and returns:
// -1 if a < b
//
//	0 if a == b
//	1 if a > b
func Compare(a, b string) int {
	return verrevcmp([]byte(a), []byte(b))
}

// verrevcmp implements version number comparison algorithm (similar to GNU strverscmp)
// Compares character and numeric segments separately, with numeric segments compared by value
func verrevcmp(s1, s2 []byte) int {
	s1Len := len(s1)
	s2Len := len(s2)
	s1Pos := 0
	s2Pos := 0

	for s1Pos < s1Len || s2Pos < s2Len {
		first_diff := 0

		// Compare non-digit portions
		for (s1Pos < s1Len && !isDigit(s1[s1Pos])) || (s2Pos < s2Len && !isDigit(s2[s2Pos])) {
			var s1c byte
			var s2c byte

			if s1Pos < s1Len {
				s1c = s1[s1Pos]
			}
			if s2Pos < s2Len {
				s2c = s2[s2Pos]
			}

			s1Order := order(s1c)
			s2Order := order(s2c)

			if s1Order != s2Order {
				return s1Order - s2Order
			}

			s1Pos++
			s2Pos++
		}

		// Skip leading zeros
		for s1Pos < s1Len && s1[s1Pos] == '0' {
			s1Pos++
		}
		for s2Pos < s2Len && s2[s2Pos] == '0' {
			s2Pos++
		}

		// Compare numeric portions
		for s1Pos < s1Len && s2Pos < s2Len && isDigit(s1[s1Pos]) && isDigit(s2[s2Pos]) {
			if first_diff == 0 {
				first_diff = int(s1[s1Pos]) - int(s2[s2Pos])
			}
			s1Pos++
			s2Pos++
		}

		// If one numeric segment is longer, that number is larger
		if s1Pos < s1Len && isDigit(s1[s1Pos]) {
			return 1
		}
		if s2Pos < s2Len && isDigit(s2[s2Pos]) {
			return -1
		}
		if first_diff != 0 {
			return first_diff
		}
	}

	return 0
}

// order returns the sorting priority of a character
// digits: 0, letters: ASCII value, '~': -1, null: 0, others: ASCII value + 256
func order(c byte) int {
	if isDigit(c) {
		return 0
	} else if isAlpha(c) {
		return int(c)
	} else if c == '~' {
		return -1
	} else if c == 0 {
		return 0
	} else {
		return int(c) + 256
	}
}

// isDigit checks if the character is a digit
func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

// isAlpha checks if the character is a letter
func isAlpha(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
