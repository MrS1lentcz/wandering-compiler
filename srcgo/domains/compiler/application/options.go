package application

import (
	"fmt"
	"io"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler"
)

// OutputModuleFactory constructs the output module from Config. The
// io.Closer return slot lets future implementations release handles on
// shutdown — the iteration-1 default returns a nil Closer.
type OutputModuleFactory func(compiler.Config) (compiler.OutputModule, io.Closer, error)

// Option mutates the appOpts before New assembles the application.
type Option func(*appOpts)

// appOpts collects the factory functions supplied by callers. Zero-value
// fields fall back to the defaults registered in New().
type appOpts struct {
	output OutputModuleFactory
}

// WithOutputModule overrides the default output-module factory (tests
// inject a fixed-directory stub; later the hosted platform's deploy
// client will inject one that reports a push target instead of a path).
func WithOutputModule(f OutputModuleFactory) Option {
	return func(o *appOpts) { o.output = f }
}

// New constructs the Application facade. The returned io.Closer is the
// same value as the returned Application — callers hold it to sequence
// shutdown (Close releases every module-registered resource in reverse
// order). Failing to construct any module returns the underlying error
// verbatim so the CLI can surface it to the user.
func New(cfg compiler.Config, opts ...Option) (compiler.Application, io.Closer, error) {
	o := &appOpts{
		output: defaultOutputModuleFactory,
	}
	for _, opt := range opts {
		opt(o)
	}

	a := &app{cfg: cfg}

	outMod, outCloser, err := o.output(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("compiler: output module: %w", err)
	}
	a.output = outMod
	if outCloser != nil {
		a.closers = append(a.closers, outCloser)
	}

	return a, a, nil
}
