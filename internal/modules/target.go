package modules

import (
	"go/ast"
	"reflect"

	classfile "github.com/goplus/llar/formula"
	"github.com/goplus/llar/internal/formula"
)

func injectMatrix(f *formula.Formula, matrix classfile.Matrix) {
	formulaElem := reflect.ValueOf(f).Elem()
	structElem := valueOf(formulaElem, "structElem").(reflect.Value)
	setValue(structElem, "target", matrix)
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
