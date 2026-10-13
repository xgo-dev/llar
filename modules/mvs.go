package modules

import (
	"github.com/goplus/llar/internal/mvs"
	"github.com/goplus/llar/mod/module"
)

var _ mvs.Reqs = (*mvsReqs)(nil)

// mvsReqs implements mvs.Reqs for module,
// with any exclusions or replacements applied internally.
type mvsReqs struct {
	roots  []module.Version
	isMain func(module.Version) bool
	cmp    func(p string, v1, v2 string) int
	onLoad func(module.Version) ([]module.Version, error)
}

func (r *mvsReqs) Required(mod module.Version) ([]module.Version, error) {
	if r.isMain(mod) {
		// Use the build list as it existed when r was constructed, not the current
		// global build list.
		return r.roots, nil
	}

	if mod.Version == "none" {
		return nil, nil
	}

	return r.onLoad(mod)
}

// Max returns the maximum of v1 and v2 according to custom comparator.
//
// As a special case, the version "" is considered higher than all other
// versions. The main module (also known as the target) has no version and must
// be chosen over other versions of the same module in the module dependency
// graph.
func (r *mvsReqs) Max(p string, v1, v2 string) string {
	if r.cmpVersion(p, v1, v2) < 0 {
		return v2
	}
	return v1
}

// Upgrade is a no-op, here to implement mvs.Reqs.
func (*mvsReqs) Upgrade(m module.Version) (module.Version, error) {
	return m, nil
}

// cmpVersion implements the comparison for versions in the module loader.
//
// As a special case, the version "" is considered higher than all other versions.
// The main module (also known as the target) has no version and must be chosen
// over other versions of the same module in the module dependency graph.
func (m *mvsReqs) cmpVersion(p string, v1, v2 string) int {
	if m.isMain(module.Version{Path: p, Version: v2}) {
		if m.isMain(module.Version{Path: p, Version: v1}) {
			return 0
		}
		return -1
	}
	if m.isMain(module.Version{Path: p, Version: v1}) {
		return 1
	}
	return m.cmp(p, v1, v2)
}
