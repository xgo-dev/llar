package formula

import (
	"maps"
	"slices"

	classfile "github.com/goplus/llar/runtime/formula"
)

// SetMatrix sets target requirements and option overrides without loading the
// formula. Formula defaults are merged after initialization; for example, an
// ssl override replaces the default ssl option while other defaults remain.
// Call SetMatrix before or between hook calls, not concurrently with them.
func (f *Formula) SetMatrix(matrix classfile.Matrix) {
	f.matrix = classfile.Matrix{
		Require: maps.Clone(matrix.Require),
		Options: maps.Clone(matrix.Options),
	}
	for key, values := range f.matrix.Require {
		f.matrix.Require[key] = slices.Clone(values)
	}
	for key, values := range f.matrix.Options {
		f.matrix.Options[key] = slices.Clone(values)
	}
	if !f.structElem.IsValid() {
		return
	}

	target := valueOf(f.structElem, "target").(classfile.Matrix)
	effective := classfile.Matrix{
		Require:        maps.Clone(f.matrix.Require),
		Options:        maps.Clone(target.DefaultOptions),
		DefaultOptions: maps.Clone(target.DefaultOptions),
	}
	if effective.Options == nil && len(f.matrix.Options) > 0 {
		effective.Options = make(map[string][]string, len(f.matrix.Options))
	}
	for key, values := range f.matrix.Options {
		effective.Options[key] = slices.Clone(values)
	}
	setValue(f.structElem, "target", effective)
}
