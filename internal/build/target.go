package build

import (
	"os"

	"github.com/goplus/llar/internal/execbroker"
)

// Command describes a command before target defaults are applied.
type Command struct {
	Name string
	Args []string
	Env  []string
	Dir  string
}

// Patch contains target-specific changes for one command.
type Patch struct {
	Name       string
	PrependArg []string
	AppendArg  []string
	Env        []string
}

// Target applies language-specific target defaults to build commands.
type Target interface {
	Use(Command) Patch
}

func targetMiddleware(target Target) execbroker.Middleware {
	return func(req execbroker.Request) (execbroker.Request, error) {
		env := req.Env
		if env == nil {
			env = os.Environ()
		}
		patch := target.Use(Command{
			Name: req.Name,
			Args: req.Args,
			Env:  env,
			Dir:  req.Dir,
		})
		if patch.Name != "" {
			req.Name = patch.Name
		}
		if len(patch.PrependArg) > 0 {
			req.Args = append(append([]string(nil), patch.PrependArg...), req.Args...)
		}
		if len(patch.AppendArg) > 0 {
			req.Args = append(req.Args, patch.AppendArg...)
		}
		if patch.Env != nil {
			req.Env = append([]string(nil), patch.Env...)
		}
		return req, nil
	}
}
