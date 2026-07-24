// export by github.com/goplus/ixgo/cmd/qexp

package cmp

import (
	q "github.com/goplus/llar/cmp"

	"go/constant"
	"reflect"

	"github.com/goplus/ixgo"
)

func init() {
	ixgo.RegisterPackage(&ixgo.Package{
		Name: "cmp",
		Path: "github.com/goplus/llar/cmp",
		Deps: map[string]string{
			"github.com/goplus/llar/mod/module": "module",
		},
		Interfaces: map[string]reflect.Type{},
		NamedTypes: map[string]reflect.Type{
			"CmpApp": reflect.TypeOf((*q.CmpApp)(nil)).Elem(),
		},
		AliasTypes: map[string]reflect.Type{},
		Vars:       map[string]reflect.Value{},
		Funcs: map[string]reflect.Value{
			"Gopt_CmpApp_Main": reflect.ValueOf(q.Gopt_CmpApp_Main),
		},
		TypedConsts: map[string]ixgo.TypedConst{},
		UntypedConsts: map[string]ixgo.UntypedConst{
			"GopPackage": {"untyped bool", constant.MakeBool(bool(q.GopPackage))},
		},
	})
}
