package views

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// loader is the loading rule that every engine of this package follows:
//
//   - Load parses now and returns the error, so a program can refuse to start.
//   - A render on an engine that was never loaded loads it first. Renders that
//     arrive together wait for that one load.
//   - With Reload(true) every render loads again.
//
// The engine gives it one function, build, that parses everything and
// publishes the result only when the whole parse succeeded. A failed load
// therefore leaves the templates of the previous one in place.
type loader struct {
	mu sync.Mutex // serializes loads.

	// state is nil until the first load has been tried.
	state atomic.Pointer[loadState]
}

// loadState is what a render sees of the loads so far. err is set while the
// engine has nothing it can render, and says why.
type loadState struct {
	err error
}

// load runs build and returns its error. It is what Load calls.
func (l *loader) load(build func() error) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.run(build)
}

// run needs l.mu.
func (l *loader) run(build func() error) error {
	err := protect(build)

	state := l.state.Load()
	usable := state != nil && state.err == nil

	switch {
	case err == nil && !usable:
		l.state.Store(&loadState{})
	case err != nil && !usable:
		l.state.Store(&loadState{err: err})
	}

	return err
}

// ready is called by every render before it looks a template up. It returns
// nil when there are templates to render with.
func (l *loader) ready(reload bool, build func() error) error {
	if reload {
		return l.load(build)
	}

	if state := l.state.Load(); state != nil {
		return state.err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Another request loaded while this one waited for the lock.
	if state := l.state.Load(); state != nil {
		return state.err
	}

	return l.run(build)
}

// add runs fn, which records a template given as text, while no load runs.
// fn is told whether a load has been tried. If one has, fn builds the engine
// again with the new template: it can then be rendered at once, and a
// template the engine cannot load with is refused. If none has, fn only
// checks the template, and the first load picks it up.
//
// fn changes nothing when it returns an error.
func (l *loader) add(fn func(attempted bool) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	attempted := l.state.Load() != nil

	err := protect(func() error {
		return fn(attempted)
	})
	if err == nil && attempted {
		l.state.Store(&loadState{})
	}

	return err
}

// loaded reports whether a load has succeeded.
func (l *loader) loaded() bool {
	state := l.state.Load()
	return state != nil && state.err == nil
}

// protect runs fn and turns a panic into an error. The parsers behind the
// engines panic on input they do not accept (html/template on a value that is
// not a function, raymond on a helper with two results), and a template must
// never take the process down with it.
func protect(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = fmt.Errorf("views: %w", e)
				return
			}

			err = fmt.Errorf("views: %v", r)
		}
	}()

	return fn()
}
