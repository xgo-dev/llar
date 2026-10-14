// export by github.com/goplus/ixgo/cmd/qexp

package semver

import (
	q "golang.org/x/mod/semver"

	"reflect"

	"github.com/goplus/ixgo"
)

func init() {
	ixgo.RegisterPackage(&ixgo.Package{
		Name: "semver",
		Path: "golang.org/x/mod/semver",
		Deps: map[string]string{
			"slices":  "slices",
			"strings": "strings",
		},
		Interfaces: map[string]reflect.Type{},
		NamedTypes: map[string]reflect.Type{
			"ByVersion": reflect.TypeOf((*q.ByVersion)(nil)).Elem(),
		},
		AliasTypes: map[string]reflect.Type{},
		Vars:       map[string]reflect.Value{},
		Funcs: map[string]reflect.Value{
			"Build":      reflect.ValueOf(q.Build),
			"Canonical":  reflect.ValueOf(q.Canonical),
			"Compare":    reflect.ValueOf(q.Compare),
			"IsValid":    reflect.ValueOf(q.IsValid),
			"Major":      reflect.ValueOf(q.Major),
			"MajorMinor": reflect.ValueOf(q.MajorMinor),
			"Max":        reflect.ValueOf(q.Max),
			"Prerelease": reflect.ValueOf(q.Prerelease),
			"Sort":       reflect.ValueOf(q.Sort),
		},
		TypedConsts:   map[string]ixgo.TypedConst{},
		UntypedConsts: map[string]ixgo.UntypedConst{},
	})
}
