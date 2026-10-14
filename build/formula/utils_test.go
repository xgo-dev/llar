// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package formula

import (
	"reflect"
	"testing"

	"github.com/goplus/ixgo"
	"github.com/goplus/ixgo/xgobuild"
	"github.com/goplus/xgo/parser"
	"github.com/goplus/xgo/parser/fsx"
	"github.com/goplus/xgo/token"
)

type testStruct struct {
	ExportedField   *string
	unexportedField *int
}

type testStructNonPtr struct {
	ExportedField   string
	unexportedField int
}

func TestValueOf(t *testing.T) {
	str := "test"
	num := 42
	ts := testStruct{ExportedField: &str, unexportedField: &num}
	elem := reflect.ValueOf(&ts).Elem()

	// Exported pointer field - returns dereferenced value
	if got := valueOf(elem, "ExportedField").(string); got != str {
		t.Errorf("valueOf(ExportedField) = %v, want %v", got, str)
	}

	// Unexported pointer field - returns pointer
	if got := valueOf(elem, "unexportedField").(*int); *got != num {
		t.Errorf("valueOf(unexportedField) = %v, want %v", *got, num)
	}

	// Non-pointer fields
	tsNonPtr := testStructNonPtr{ExportedField: str, unexportedField: num}
	elemNonPtr := reflect.ValueOf(&tsNonPtr).Elem()

	if got := valueOf(elemNonPtr, "ExportedField").(string); got != str {
		t.Errorf("valueOf(ExportedField) = %v, want %v", got, str)
	}
	if got := valueOf(elemNonPtr, "unexportedField").(int); got != num {
		t.Errorf("valueOf(unexportedField) = %v, want %v", got, num)
	}
}

func TestSetValue(t *testing.T) {
	str := "initial"
	num := 10
	ts := testStruct{ExportedField: &str, unexportedField: &num}
	elem := reflect.ValueOf(&ts).Elem()

	// Set exported pointer field
	newStr := "modified"
	setValue(elem, "ExportedField", &newStr)
	if *ts.ExportedField != newStr {
		t.Errorf("ExportedField = %v, want %v", *ts.ExportedField, newStr)
	}

	// Set unexported pointer field
	newNum := 99
	setValue(elem, "unexportedField", &newNum)
	if *ts.unexportedField != newNum {
		t.Errorf("unexportedField = %v, want %v", *ts.unexportedField, newNum)
	}

	// Non-pointer fields
	tsNonPtr := testStructNonPtr{ExportedField: "a", unexportedField: 1}
	elemNonPtr := reflect.ValueOf(&tsNonPtr).Elem()

	setValue(elemNonPtr, "ExportedField", "b")
	if tsNonPtr.ExportedField != "b" {
		t.Errorf("ExportedField = %v, want b", tsNonPtr.ExportedField)
	}
	setValue(elemNonPtr, "unexportedField", 2)
	if tsNonPtr.unexportedField != 2 {
		t.Errorf("unexportedField = %v, want 2", tsNonPtr.unexportedField)
	}
}

func TestSetValueNil(t *testing.T) {
	str := "initial"
	num := 42
	ts := testStruct{ExportedField: &str, unexportedField: &num}
	elem := reflect.ValueOf(&ts).Elem()

	setValue(elem, "ExportedField", nil)
	if ts.ExportedField != nil {
		t.Errorf("ExportedField = %v, want nil", ts.ExportedField)
	}

	setValue(elem, "unexportedField", nil)
	if ts.unexportedField != nil {
		t.Errorf("unexportedField = %v, want nil", ts.unexportedField)
	}

	// Non-pointer fields - nil sets to zero value
	tsNonPtr := testStructNonPtr{ExportedField: "a", unexportedField: 1}
	elemNonPtr := reflect.ValueOf(&tsNonPtr).Elem()

	setValue(elemNonPtr, "ExportedField", nil)
	if tsNonPtr.ExportedField != "" {
		t.Errorf("ExportedField = %v, want empty", tsNonPtr.ExportedField)
	}
	setValue(elemNonPtr, "unexportedField", nil)
	if tsNonPtr.unexportedField != 0 {
		t.Errorf("unexportedField = %v, want 0", tsNonPtr.unexportedField)
	}
}

func TestUnexportValueOf(t *testing.T) {
	num := 123
	ts := testStruct{unexportedField: &num}
	elem := reflect.ValueOf(&ts).Elem()
	field := elem.FieldByName("unexportedField")

	val := unexportValueOf(field)
	if !val.CanInterface() {
		t.Error("unexportValueOf should return interfaceable value")
	}
	if got := val.Interface().(*int); *got != num {
		t.Errorf("got %v, want %v", *got, num)
	}
}

func TestClassfile(t *testing.T) {
	t.Run("ixgo", func(t *testing.T) {
		ctx := ixgo.NewContext(0)
		xgoContext := xgobuild.NewContext(ctx)
		if _, err := xgoContext.ParseFile("testdata/formula/hello_llar.gox", nil); err != nil {
			t.Error(err)
		}
	})

	t.Run("xgo", func(t *testing.T) {
		fs := token.NewFileSet()
		_, err := parser.ParseFSEntry(fs, fsx.Local, "testdata/formula/hello_llar.gox", nil, parser.Config{
			ClassKind: xgobuild.ClassKind,
		})
		if err != nil {
			t.Error(err)
		}
	})
}
