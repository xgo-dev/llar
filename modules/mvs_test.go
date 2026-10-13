package modules

import (
	"testing"

	"github.com/goplus/llar/internal/mvs"
	"github.com/goplus/llar/mod/module"
)

func TestMvsReqs_Max(t *testing.T) {
	reqs := &mvsReqs{
		isMain: func(v module.Version) bool {
			return v.Path == "main" && v.Version == ""
		},
		cmp: func(p, v1, v2 string) int {
			// simple string comparison for test
			if v1 < v2 {
				return -1
			} else if v1 > v2 {
				return 1
			}
			return 0
		},
	}

	tests := []struct {
		name string
		p    string
		v1   string
		v2   string
		want string
	}{
		{"v1 > v2", "pkg", "v2.0.0", "v1.0.0", "v2.0.0"},
		{"v1 < v2", "pkg", "v1.0.0", "v2.0.0", "v2.0.0"},
		{"v1 == v2", "pkg", "v1.0.0", "v1.0.0", "v1.0.0"},
		{"main version wins", "main", "", "v1.0.0", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reqs.Max(tt.p, tt.v1, tt.v2)
			if got != tt.want {
				t.Errorf("Max(%q, %q, %q) = %q, want %q", tt.p, tt.v1, tt.v2, got, tt.want)
			}
		})
	}
}

func TestMvsReqs_Required(t *testing.T) {
	roots := []module.Version{
		{Path: "dep1", Version: "v1.0.0"},
		{Path: "dep2", Version: "v2.0.0"},
	}

	reqs := &mvsReqs{
		roots: roots,
		isMain: func(v module.Version) bool {
			return v.Path == "main" && v.Version == ""
		},
		onLoad: func(mod module.Version) ([]module.Version, error) {
			if mod.Path == "dep1" {
				return []module.Version{{Path: "dep3", Version: "v1.0.0"}}, nil
			}
			return nil, nil
		},
	}

	t.Run("main module returns roots", func(t *testing.T) {
		got, err := reqs.Required(module.Version{Path: "main", Version: ""})
		if err != nil {
			t.Fatalf("Required() error = %v", err)
		}
		if len(got) != len(roots) {
			t.Errorf("Required() returned %d deps, want %d", len(got), len(roots))
		}
	})

	t.Run("none version returns nil", func(t *testing.T) {
		got, err := reqs.Required(module.Version{Path: "dep1", Version: "none"})
		if err != nil {
			t.Fatalf("Required() error = %v", err)
		}
		if got != nil {
			t.Errorf("Required() = %v, want nil", got)
		}
	})

	t.Run("regular module calls onLoad", func(t *testing.T) {
		got, err := reqs.Required(module.Version{Path: "dep1", Version: "v1.0.0"})
		if err != nil {
			t.Fatalf("Required() error = %v", err)
		}
		if len(got) != 1 || got[0].Path != "dep3" {
			t.Errorf("Required() = %v, want [{dep3 v1.0.0}]", got)
		}
	})
}

func TestMvsReqs_cmpVersion(t *testing.T) {
	reqs := &mvsReqs{
		isMain: func(v module.Version) bool {
			return v.Path == "main" && v.Version == ""
		},
		cmp: func(p, v1, v2 string) int {
			if v1 < v2 {
				return -1
			} else if v1 > v2 {
				return 1
			}
			return 0
		},
	}

	tests := []struct {
		name string
		p    string
		v1   string
		v2   string
		want int
	}{
		{"v1 < v2", "pkg", "v1.0.0", "v2.0.0", -1},
		{"v1 > v2", "pkg", "v2.0.0", "v1.0.0", 1},
		{"v1 == v2", "pkg", "v1.0.0", "v1.0.0", 0},
		{"main v2 wins over v1", "main", "v1.0.0", "", -1},
		{"v1 wins over main v2 (v1 is main)", "main", "", "v1.0.0", 1},
		{"both main", "main", "", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reqs.cmpVersion(tt.p, tt.v1, tt.v2)
			if got != tt.want {
				t.Errorf("cmpVersion(%q, %q, %q) = %d, want %d", tt.p, tt.v1, tt.v2, got, tt.want)
			}
		})
	}
}

func TestMvsReqs_Upgrade(t *testing.T) {
	reqs := &mvsReqs{}

	mod := module.Version{Path: "test/pkg", Version: "v1.0.0"}
	got, err := reqs.Upgrade(mod)
	if err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}
	if got != mod {
		t.Errorf("Upgrade() = %v, want %v (no-op)", got, mod)
	}
}

func TestMVS_BuildList(t *testing.T) {
	// Simulate:
	// A@1.0 -> B@1.0, C@1.0
	// B@1.0 -> C@2.0
	// C@1.0 -> (none)
	// C@2.0 -> (none)
	// MVS should select: A@1.0, B@1.0, C@2.0

	deps := map[module.Version][]module.Version{
		{Path: "A", Version: "1.0"}: {
			{Path: "B", Version: "1.0"},
			{Path: "C", Version: "1.0"},
		},
		{Path: "B", Version: "1.0"}: {
			{Path: "C", Version: "2.0"},
		},
		{Path: "C", Version: "1.0"}: {},
		{Path: "C", Version: "2.0"}: {},
	}

	main := module.Version{Path: "A", Version: "1.0"}

	reqs := &mvsReqs{
		roots: deps[main],
		isMain: func(v module.Version) bool {
			return v.Path == main.Path && v.Version == main.Version
		},
		cmp: func(p, v1, v2 string) int {
			if v1 == "none" {
				return -1
			}
			if v2 == "none" {
				return 1
			}
			if v1 < v2 {
				return -1
			} else if v1 > v2 {
				return 1
			}
			return 0
		},
		onLoad: func(mod module.Version) ([]module.Version, error) {
			if d, ok := deps[mod]; ok {
				return d, nil
			}
			return nil, nil
		},
	}

	buildList, err := mvs.BuildList([]module.Version{main}, reqs)
	if err != nil {
		t.Fatalf("BuildList() error = %v", err)
	}

	// Should have 3 modules: A, B, C
	if len(buildList) != 3 {
		t.Fatalf("BuildList() returned %d modules, want 3: %v", len(buildList), buildList)
	}

	// First should be main
	if buildList[0] != main {
		t.Errorf("BuildList()[0] = %v, want %v", buildList[0], main)
	}

	// C should be version 2.0 (MVS selects max)
	cVersion := ""
	for _, m := range buildList {
		if m.Path == "C" {
			cVersion = m.Version
			break
		}
	}
	if cVersion != "2.0" {
		t.Errorf("C version = %q, want %q (MVS should select max)", cVersion, "2.0")
	}
}

func TestMVS_DiamondDependency(t *testing.T) {
	// Diamond dependency:
	// A -> B, C
	// B -> D@1.0
	// C -> D@2.0
	// MVS should select D@2.0

	deps := map[module.Version][]module.Version{
		{Path: "A", Version: "1.0"}: {
			{Path: "B", Version: "1.0"},
			{Path: "C", Version: "1.0"},
		},
		{Path: "B", Version: "1.0"}: {
			{Path: "D", Version: "1.0"},
		},
		{Path: "C", Version: "1.0"}: {
			{Path: "D", Version: "2.0"},
		},
		{Path: "D", Version: "1.0"}: {},
		{Path: "D", Version: "2.0"}: {},
	}

	main := module.Version{Path: "A", Version: "1.0"}

	reqs := &mvsReqs{
		roots: deps[main],
		isMain: func(v module.Version) bool {
			return v.Path == main.Path && v.Version == main.Version
		},
		cmp: func(p, v1, v2 string) int {
			if v1 == "none" {
				return -1
			}
			if v2 == "none" {
				return 1
			}
			if v1 < v2 {
				return -1
			} else if v1 > v2 {
				return 1
			}
			return 0
		},
		onLoad: func(mod module.Version) ([]module.Version, error) {
			if d, ok := deps[mod]; ok {
				return d, nil
			}
			return nil, nil
		},
	}

	buildList, err := mvs.BuildList([]module.Version{main}, reqs)
	if err != nil {
		t.Fatalf("BuildList() error = %v", err)
	}

	// Find D version
	dVersion := ""
	for _, m := range buildList {
		if m.Path == "D" {
			dVersion = m.Version
			break
		}
	}
	if dVersion != "2.0" {
		t.Errorf("D version = %q, want %q (MVS diamond: select max)", dVersion, "2.0")
	}
}

func TestMVS_NoneVersion(t *testing.T) {
	// Test that "none" version is handled correctly
	deps := map[module.Version][]module.Version{
		{Path: "A", Version: "1.0"}: {
			{Path: "B", Version: "1.0"},
		},
		{Path: "B", Version: "1.0"}: {},
	}

	main := module.Version{Path: "A", Version: "1.0"}

	reqs := &mvsReqs{
		roots: deps[main],
		isMain: func(v module.Version) bool {
			return v.Path == main.Path && v.Version == main.Version
		},
		cmp: func(p, v1, v2 string) int {
			if v1 == "none" && v2 != "none" {
				return -1
			}
			if v1 != "none" && v2 == "none" {
				return 1
			}
			if v1 == "none" && v2 == "none" {
				return 0
			}
			if v1 < v2 {
				return -1
			} else if v1 > v2 {
				return 1
			}
			return 0
		},
		onLoad: func(mod module.Version) ([]module.Version, error) {
			if d, ok := deps[mod]; ok {
				return d, nil
			}
			return nil, nil
		},
	}

	// Test Max with "none"
	if got := reqs.Max("B", "1.0", "none"); got != "1.0" {
		t.Errorf("Max(1.0, none) = %q, want %q", got, "1.0")
	}
	if got := reqs.Max("B", "none", "1.0"); got != "1.0" {
		t.Errorf("Max(none, 1.0) = %q, want %q", got, "1.0")
	}
}
