package modules

import (
	"go/ast"
	"maps"
	"reflect"
	"slices"

	classfile "github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/formula"
)

func injectMatrix(f *formula.Formula, matrix classfile.Matrix) {
	formulaElem := reflect.ValueOf(f).Elem()
	structElem := valueOf(formulaElem, "structElem").(reflect.Value)
	target := valueOf(structElem, "target").(classfile.Matrix)

	effective := classfile.Matrix{
		Require:        maps.Clone(matrix.Require),
		Options:        maps.Clone(target.DefaultOptions),
		DefaultOptions: maps.Clone(target.DefaultOptions),
	}
	if effective.Options == nil && len(matrix.Options) > 0 {
		effective.Options = make(map[string][]string, len(matrix.Options))
	}
	for key, values := range matrix.Options {
		effective.Options[key] = slices.Clone(values)
	}
	setValue(structElem, "target", effective)
}

func setValue(elem reflect.Value, name string, value any) {
	field := elem.FieldByName(name)
	if !ast.IsExported(name) {
		field = unexportValueOf(field)
	}

	var val reflect.Value
	if value == nil {
		val = reflect.Zero(field.Type())
	} else {
		val = reflect.ValueOf(value)
	}
	field.Set(val)
}
