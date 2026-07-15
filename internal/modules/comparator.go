package modules

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"unsafe"

	"github.com/goplus/ixgo"
	"github.com/goplus/ixgo/xgobuild"
	llarixgo "github.com/goplus/llar/internal/ixgo"
	"github.com/goplus/llar/mod/module"
)

type comparatorProgram struct {
	typ reflect.Type
}

// loadComparator loads a version comparator from a .gox file at the given path.
// Returns an error if the file cannot be loaded or parsed.
//
// The returned comparator compares two module versions and returns:
//   - a negative value if v1 < v2
//   - zero if v1 == v2
//   - a positive value if v1 > v2
func loadComparatorFS(fs fs.ReadFileFS, path string) (comparator func(v1, v2 module.Version) int, err error) {
	llarixgo.LockInterp()
	defer llarixgo.UnlockInterp()

	// Loading a comparator must not reset method slots owned by cached formulas.
	ctx := ixgo.NewContext(ixgo.SupportMultipleInterp)

	content, err := fs.ReadFile(path)
	if err != nil {
		return nil, err
	}

	source, err := xgobuild.BuildFile(ctx, path, content)
	if err != nil {
		return nil, err
	}
	pkgs, err := ctx.LoadFile("main.go", source)
	if err != nil {
		return nil, err
	}
	interp, err := ctx.NewInterp(pkgs)
	if err != nil {
		return nil, err
	}
	program := &comparatorProgram{}
	runtime.AddCleanup(program, llarixgo.ReleaseInterp, interp)
	if err = interp.RunInit(); err != nil {
		return nil, err
	}
	structName, _, ok := strings.Cut(filepath.Base(path), "_")
	if !ok {
		return nil, fmt.Errorf("failed to load: file name is not valid: %s", path)
	}
	typ, ok := interp.GetType(structName)
	if !ok {
		return nil, fmt.Errorf("failed to load: struct name not found: %s", structName)
	}
	program.typ = typ
	val := reflect.New(typ)
	class := val.Elem()

	val.Interface().(interface{ Main() }).Main()

	compare := valueOf(class, "fCompareVer").(func(v1, v2 module.Version) int)
	return func(v1, v2 module.Version) int {
		result := compare(v1, v2)
		runtime.KeepAlive(program)
		return result
	}, nil
}

// unexportValueOf creates a reflect.Value that allows access to unexported fields.
// It uses unsafe operations to bypass Go's exported field restrictions.
func unexportValueOf(field reflect.Value) reflect.Value {
	return reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
}

func valueOf(elem reflect.Value, name string) any {
	return unexportValueOf(elem.FieldByName(name)).Interface()
}
