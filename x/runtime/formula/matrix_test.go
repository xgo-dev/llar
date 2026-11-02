// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package formula

import (
	"testing"
)

func TestMatrix_CombinationCount(t *testing.T) {
	tests := []struct {
		name   string
		matrix Matrix
		want   int
	}{
		{
			name: "require only",
			matrix: Matrix{
				Require: map[string][]string{
					"os":   {"linux", "darwin"},
					"arch": {"x86_64", "arm64"},
					"lang": {"c", "cpp"},
				},
			},
			want: 8, // 2 * 2 * 2
		},
		{
			name: "require with options",
			matrix: Matrix{
				Require: map[string][]string{
					"os":   {"linux"},
					"arch": {"x86_64", "arm64"},
				},
				Options: map[string][]string{
					"zlib": {"zlibON", "zlibOFF"},
				},
			},
			want: 4, // 2 * 1 * 2
		},
		{
			name: "only options",
			matrix: Matrix{
				Options: map[string][]string{
					"zlib": {"zlibON", "zlibOFF"},
					"ssl":  {"sslON"},
				},
			},
			want: 2, // 2 * 1
		},
		{
			name:   "empty matrix",
			matrix: Matrix{},
			want:   0,
		},
		{
			name: "large combination",
			matrix: Matrix{
				Require: map[string][]string{
					"os":   {"linux", "darwin", "windows"},
					"arch": {"x86_64", "arm64"},
					"lang": {"c", "cpp", "rust"},
				},
				Options: map[string][]string{
					"zlib": {"zlibON", "zlibOFF"},
					"ssl":  {"sslON", "sslOFF"},
				},
			},
			want: 72, // 3 * 2 * 3 * 2 * 2
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.matrix.CombinationCount()
			if got != tt.want {
				t.Errorf("Matrix.CombinationCount() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestMatrix_Combinations(t *testing.T) {
	tests := []struct {
		name   string
		matrix Matrix
		want   []string
	}{
		{
			name: "require only",
			matrix: Matrix{
				Require: map[string][]string{
					"os":   {"linux", "darwin"},
					"arch": {"x86_64", "arm64"},
					"lang": {"c", "cpp"},
				},
			},
			// sorted keys: arch, lang, os
			// arch x lang: x86_64-c, x86_64-cpp, arm64-c, arm64-cpp
			// then x os: x86_64-c-linux, x86_64-c-darwin, x86_64-cpp-linux, ...
			want: []string{
				"x86_64-c-linux",
				"x86_64-c-darwin",
				"x86_64-cpp-linux",
				"x86_64-cpp-darwin",
				"arm64-c-linux",
				"arm64-c-darwin",
				"arm64-cpp-linux",
				"arm64-cpp-darwin",
			},
		},
		{
			name: "require with options",
			matrix: Matrix{
				Require: map[string][]string{
					"os":   {"linux"},
					"arch": {"x86_64", "arm64"},
				},
				Options: map[string][]string{
					"zlib": {"zlibON", "zlibOFF"},
				},
			},
			// sorted keys: arch, os -> x86_64-linux, arm64-linux
			// options: zlib -> zlibON, zlibOFF
			want: []string{
				"x86_64-linux|zlibON",
				"x86_64-linux|zlibOFF",
				"arm64-linux|zlibON",
				"arm64-linux|zlibOFF",
			},
		},
		{
			name: "only options",
			matrix: Matrix{
				Options: map[string][]string{
					"zlib": {"zlibON", "zlibOFF"},
					"ssl":  {"sslON"},
				},
			},
			// sorted: ssl, zlib
			want: []string{
				"sslON-zlibON",
				"sslON-zlibOFF",
			},
		},
		{
			name:   "empty matrix",
			matrix: Matrix{},
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.matrix.Combinations()
			if len(got) != len(tt.want) {
				t.Errorf("Matrix.Combinations() length = %d, want %d", len(got), len(tt.want))
				t.Errorf("got: %v", got)
				return
			}
			for i, v := range got {
				if v != tt.want[i] {
					t.Errorf("Matrix.Combinations()[%d] = %q, want %q", i, v, tt.want[i])
				}
			}
		})
	}
}
