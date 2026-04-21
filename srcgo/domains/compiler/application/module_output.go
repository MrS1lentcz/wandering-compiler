package application

import (
	"io"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler"
)

// outputModule is a thin carrier for the resolved output directory.
// Every attribute is set at factory time from Config — there is no
// runtime state.
type outputModule struct {
	dir string
}

func (m *outputModule) OutputDir() string { return m.dir }

// defaultOutputModuleFactory pulls the directory from Config. Keeping
// the wrapper even with a trivial implementation means the point of
// override (tests, future platform client, …) already exists — later
// factories only change the body, not the call site.
func defaultOutputModuleFactory(cfg compiler.Config) (compiler.OutputModule, io.Closer, error) {
	return &outputModule{dir: cfg.OutputDir}, nil, nil
}
