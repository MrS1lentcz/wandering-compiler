package application_test

import (
	"errors"
	"io"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/application"
)

// TestNewAndConfig exercises the happy-path DI wiring: New plumbs
// Config through, the default output factory returns Config's
// OutputDir, and Close is a no-op on the iteration-1 default
// module (nil Closer).
func TestNewAndConfig(t *testing.T) {
	cfg := compiler.Config{OutputDir: "/tmp/out"}
	app, closer, err := application.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer closer.Close()

	if got := app.Config().OutputDir; got != cfg.OutputDir {
		t.Errorf("Config().OutputDir = %q, want %q", got, cfg.OutputDir)
	}
	if got := app.OutputDir(); got != cfg.OutputDir {
		t.Errorf("OutputDir() = %q, want %q", got, cfg.OutputDir)
	}
}

// fakeCloser counts Close() calls so the test can verify Close()
// reaches every registered closer exactly once, in reverse order.
type fakeCloser struct {
	closed int
	err    error
	tag    string
	order  *[]string
}

func (f *fakeCloser) Close() error {
	f.closed++
	if f.order != nil {
		*f.order = append(*f.order, f.tag)
	}
	return f.err
}

// TestCloseReleasesModules proves Close() calls every registered
// closer in reverse registration order (LIFO) and surfaces the
// first non-nil error.
func TestCloseReleasesModules(t *testing.T) {
	var order []string
	closerA := &fakeCloser{tag: "A", order: &order}

	cfg := compiler.Config{OutputDir: "/tmp/a"}
	app, shutdown, err := application.New(cfg,
		application.WithOutputModule(func(c compiler.Config) (compiler.OutputModule, io.Closer, error) {
			return stubOutput{dir: c.OutputDir}, closerA, nil
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := app.OutputDir(); got != "/tmp/a" {
		t.Errorf("OutputDir = %q, want /tmp/a", got)
	}
	if err := shutdown.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if closerA.closed != 1 {
		t.Errorf("closerA.closed = %d, want 1", closerA.closed)
	}
}

// TestCloseSurfacesFirstError — Close must not swallow errors from
// registered closers; the first non-nil error propagates up.
func TestCloseSurfacesFirstError(t *testing.T) {
	want := errors.New("boom")
	c := &fakeCloser{err: want}

	_, shutdown, err := application.New(compiler.Config{},
		application.WithOutputModule(func(_ compiler.Config) (compiler.OutputModule, io.Closer, error) {
			return stubOutput{}, c, nil
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := shutdown.Close(); got != want {
		t.Errorf("Close = %v, want %v", got, want)
	}
}

// TestNewPropagatesFactoryError — an output-module factory failure
// fails New() verbatim (wrapped) so the CLI surfaces it to the
// user without any DI layer swallowing it.
func TestNewPropagatesFactoryError(t *testing.T) {
	boom := errors.New("factory boom")
	_, _, err := application.New(compiler.Config{},
		application.WithOutputModule(func(_ compiler.Config) (compiler.OutputModule, io.Closer, error) {
			return nil, nil, boom
		}),
	)
	if err == nil {
		t.Fatal("New: expected error, got nil")
	}
	if !errors.Is(err, boom) {
		t.Errorf("err chain does not contain boom: %v", err)
	}
}

// stubOutput is a minimal OutputModule for factory-override tests.
type stubOutput struct{ dir string }

func (s stubOutput) OutputDir() string { return s.dir }
