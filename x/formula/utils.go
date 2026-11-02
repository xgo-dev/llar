// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package formula

import (
	"go/ast"
	"reflect"
	"unsafe"
)

// unexportValueOf creates a reflect.Value that allows access to unexported fields.
// It uses unsafe operations to bypass Go's exported field restrictions.
func unexportValueOf(field reflect.Value) reflect.Value {
	return reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
}

// valueOf retrieves the value of a field by name from a struct element.
// It handles both exported and unexported fields, and both pointer and non-pointer types.
// For pointer fields, it returns the dereferenced value; for non-pointer fields, it returns the value directly.
func valueOf(elem reflect.Value, name string) any {
	if ast.IsExported(name) {
		field := elem.FieldByName(name)
		if field.Kind() == reflect.Ptr {
			return field.Elem().Interface()
		}
		return field.Interface()
	}
	return unexportValueOf(elem.FieldByName(name)).Interface()
}

// setValue sets the value of a field by name in a struct element.
// It handles both exported and unexported fields, and nil values.
// For nil values, it creates a zero value of the field's type.
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
