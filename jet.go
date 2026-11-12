package views

import (
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/kataras/iris/v14/context"
	"github.com/kataras/iris/v14/middleware/view"

	"github.com/CloudyKit/jet/v6"
)

const jetEngineName = "jet"

// JetEngine is the jet template parser's view engine.
type JetEngine struct {
	fs          fs.FS
	rootDir     string
	extension   string
	left, right string

	// userLoader is the loader given to SetLoader, nil for the file system.
	userLoader jet.Loader

	developmentMode bool

	// The Set is the `*jet.Set`, exported to offer any custom capabilities that jet users may want.
	// It is nil until the first Load, ParseTemplate or render.
	Set *jet.Set

	// mu guards Set (its creation), vars and custom. A request can add a
	// function, through view.AddFunc, while other requests render.
	mu sync.Mutex

	// Note that global vars and functions are set in a single spot on the jet parser.
	// The ones added before the Set exists wait here.
	vars map[string]any

	// custom holds the templates of ParseTemplate. The loader of the Set
	// looks here first.
	custom map[string]string

	loader loader
	cache  jetCache

	// reads counts the templates read for the one that is being parsed,
	// while limited is set. jet parses a template that extends itself
	// until the memory runs out.
	limited atomic.Bool
	reads   atomic.Int64

	jetDataContextKey string
}

var (
	_ view.Engine         = (*JetEngine)(nil)
	_ view.EngineFuncer   = (*JetEngine)(nil)
	_ view.EngineExtenser = (*JetEngine)(nil)
)

// jet library does not export or give us any option to modify them via Set
// (unless we parse the files by ourselves but this is not a smart choice).
var jetExtensions = [...]string{
	".html.jet",
	".jet.html",
	".jet",
}

// Jet creates and returns a new jet view engine.
// The given "extension" MUST begin with a dot.
// It panics for an extension other than ".jet", ".html.jet" and ".jet.html".
//
// A Jet template names its own parent with {{ extends }}, so the layout
// argument of ExecuteTemplate is ignored by this engine.
//
// Usage:
// Jet("./views", ".jet") or
// Jet(embed.FS, ".jet").RootDir("views") for embedded templates.
func Jet(dirOrFS any, extension string) *JetEngine {
	extOK := false
	for _, ext := range jetExtensions {
		if ext == extension {
			extOK = true
			break
		}
	}

	if !extOK {
		panic(fmt.Sprintf("%s extension is not a valid jet engine extension[%s]", extension, strings.Join(jetExtensions[0:], ", ")))
	}

	s := &JetEngine{
		fs:                getFS(dirOrFS),
		rootDir:           "/",
		extension:         extension,
		jetDataContextKey: "_jet",
	}

	return s
}

// String returns the name of this view engine, the "jet".
func (s *JetEngine) String() string {
	return jetEngineName
}

// RootDir sets the directory to be used as a starting point
// to load templates from the provided file system.
// It is what points the engine at a directory of an embed.FS.
//
// It panics if root is not a valid path inside the file system.
func (s *JetEngine) RootDir(root string) *JetEngine {
	root = filepath.ToSlash(root)
	if root != s.rootDir {
		s.fs = subFS(s.fs, root)
	}

	s.rootDir = root
	return s
}

// Name returns the jet engine's name.
func (s *JetEngine) Name() string {
	return "Jet"
}

// Ext should return the final file extension which this view engine is responsible to render.
// If the filename extension on ExecuteTemplate is empty then this is appended.
func (s *JetEngine) Ext() string {
	return s.extension
}

// Delims sets the action delimiters to the specified strings, to be used in
// templates. An empty delimiter stands for the
// corresponding default: {{ or }}.
// Should act before `Load` or `view.Handler`.
func (s *JetEngine) Delims(left, right string) *JetEngine {
	s.left = left
	s.right = right
	return s
}

// JetArguments is a type alias of `jet.Arguments`,
// can be used on `AddFunc$funcBody`.
type JetArguments = jet.Arguments

// AddFunc should adds a global function to the jet template set.
// It takes effect at once, loaded or not.
//
// The function can be a jet.Func, a func(JetArguments) reflect.Value, or
// any other Go function, which jet calls with the arguments of the template.
func (s *JetEngine) AddFunc(funcName string, funcBody any) {
	switch fn := funcBody.(type) {
	case jet.Func:
		s.AddVar(funcName, fn)
	case func(JetArguments) reflect.Value:
		s.AddVar(funcName, jet.Func(fn))
	case func(string, ...any) string:
		// if something like "urlpath" is registered.
		// jet does not spread its arguments over a variadic of any,
		// so the call is made here.
		s.AddVar(funcName, jet.Func(func(args JetArguments) reflect.Value {
			n := args.NumOfArguments()
			if n == 0 { // no input, don't execute the function, panic instead.
				panic(fmt.Errorf("%s expects one or more input arguments", funcName))
			}

			firstInput := args.Get(0).String()

			if n == 1 { // if only the first argument is given.
				return reflect.ValueOf(fn(firstInput))
			}

			// if has variadic.

			variadicN := n - 1
			variadicInputs := make([]any, variadicN) // except the first one.

			for i := 0; i < variadicN; i++ {
				variadicInputs[i] = args.Get(i + 1).Interface()
			}

			return reflect.ValueOf(fn(firstInput, variadicInputs...))
		}))
	default:
		// An ordinary function, which jet calls through reflection.
		s.AddVar(funcName, funcBody)
	}
}

// AddVar adds a global variable to the jet template set.
// It takes effect at once, loaded or not.
func (s *JetEngine) AddVar(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Set != nil {
		s.Set.AddGlobal(key, value)
		return
	}

	if s.vars == nil {
		s.vars = make(map[string]any)
	}
	s.vars[key] = value
}

// Reload if setted to true the templates are reloading on each render,
// use it when you're in development and you don't want to restart
// the whole app when you edit a template file.
//
// Every render then parses all templates first, as in the other engines,
// and jet is put in its development mode, in which it reads the template
// it renders again. Call it before the engine loads.
// Do not use it in production.
func (s *JetEngine) Reload(developmentMode bool) *JetEngine {
	s.developmentMode = developmentMode
	return s
}

// SetLoader can be used when the caller wants to use something like
// multi.Loader or httpfs.Loader.
// The templates are then read from that loader and not from the file
// system given to Jet.
func (s *JetEngine) SetLoader(loader jet.Loader) *JetEngine {
	s.userLoader = loader
	return s
}

// jetLoader is the loader of the Set of an engine. It reads the templates
// of ParseTemplate first and then the loader of SetLoader or, without one,
// the file system of the engine.
type jetLoader struct {
	engine *JetEngine
}

var _ jet.Loader = (*jetLoader)(nil)

func (l *jetLoader) customTemplate(name string) (string, bool) {
	l.engine.mu.Lock()
	defer l.engine.mu.Unlock()

	contents, ok := l.engine.custom[name]
	return contents, ok
}

// Open opens a file from file system.
func (l *jetLoader) Open(name string) (io.ReadCloser, error) {
	if l.engine.limited.Load() && l.engine.reads.Add(1) > maxTemplateReads {
		return nil, errTooManyReads
	}

	if contents, ok := l.customTemplate(name); ok {
		return io.NopCloser(strings.NewReader(contents)), nil
	}

	if l.engine.userLoader != nil {
		return l.engine.userLoader.Open(name)
	}

	return l.engine.fs.Open(strings.TrimPrefix(name, "/"))
}

// Exists checks if the template name exists by walking the list of template paths.
func (l *jetLoader) Exists(name string) bool {
	if _, ok := l.customTemplate(name); ok {
		return true
	}

	if l.engine.userLoader != nil {
		return l.engine.userLoader.Exists(name)
	}

	// fs.Stat closes the file it opens. A directory is not a template.
	info, err := fs.Stat(l.engine.fs, strings.TrimPrefix(name, "/"))
	return err == nil && !info.IsDir()
}

// jetCache is the template cache of the Set of an engine. It is jet's own
// cache plus a way to empty it, which is how Load makes the engine read the
// files again.
type jetCache struct {
	mu        sync.RWMutex
	templates map[string]*jet.Template
}

var _ jet.Cache = (*jetCache)(nil)

// Get returns a cached template, nil if there is none.
func (c *jetCache) Get(templatePath string) *jet.Template {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.templates[templatePath]
}

// Put caches a template.
func (c *jetCache) Put(templatePath string, t *jet.Template) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.templates == nil {
		c.templates = make(map[string]*jet.Template)
	}
	c.templates[templatePath] = t
}

func (c *jetCache) reset() {
	c.mu.Lock()
	c.templates = nil
	c.mu.Unlock()
}

// Load parses every template, to find the ones that do not parse, and
// empties the cache, so that each template is read again when it is next
// rendered.
//
// Calling it is optional: an engine that was never loaded loads itself on
// its first ExecuteTemplate. Call it at startup to find a template that does
// not parse before the first request does.
func (s *JetEngine) Load() error {
	return s.loader.load(s.build)
}

// IsLoaded reports whether the templates are loaded at least once or not.
func (s *JetEngine) IsLoaded() bool {
	return s.loader.loaded()
}

// ParseTemplate accepts a name and contnets to parse and cache a template.
// This parser does not support funcs per template. Use the `AddFunc` instead.
//
// When it returns an error, nothing was added.
func (s *JetEngine) ParseTemplate(name string, contents string) error {
	// The loader is asked for absolute names.
	key := path.Join("/", filepath.ToSlash(name))

	return s.loader.add(func(attempted bool) error {
		// Parse it first, so that a template that does not parse is refused.
		if err := s.check(name, contents); err != nil {
			return err
		}

		s.mu.Lock()
		previous, existed := s.custom[key]
		if s.custom == nil {
			s.custom = make(map[string]string)
		}
		s.custom[key] = contents
		s.mu.Unlock()

		// A template of this name may have been rendered already.
		s.cache.reset()

		if !attempted {
			return nil
		}

		// Loaded already: load again with it.
		err := s.build()
		if err != nil {
			s.mu.Lock()
			if existed {
				s.custom[key] = previous
			} else {
				delete(s.custom, key)
			}
			s.mu.Unlock()

			s.cache.reset()
		}

		return err
	})
}

// check parses one template without keeping the result. The loader calls
// it and ParseTemplate, one call at a time.
func (s *JetEngine) check(name, contents string) error {
	set := s.set()

	s.reads.Store(0)
	s.limited.Store(true)
	defer s.limited.Store(false)

	_, err := set.Parse(name, contents)
	if s.reads.Load() > maxTemplateReads {
		// The error of jet is as deep as the recursion was.
		return errTooManyReads
	}

	return err
}

// set returns the jet set, which is created on first use: its options are
// final only after the engine is configured.
func (s *JetEngine) set() *jet.Set {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Set == nil {
		opts := []jet.Option{
			jet.WithDelims(s.left, s.right),
			jet.WithCache(&s.cache),
		}
		if s.developmentMode {
			opts = append(opts, jet.InDevelopmentMode())
		}

		s.Set = jet.NewSet(&jetLoader{engine: s}, opts...)
		for key, value := range s.vars {
			s.Set.AddGlobal(key, value)
		}
		s.vars = nil
	}

	return s.Set
}

// build parses every template of the file system. jet reads a template
// itself when it is rendered, so the result is not kept: the point is to
// report the templates that do not parse. The loader calls it, one call at
// a time.
func (s *JetEngine) build() error {
	files, err := readTemplates(s.fs, s.extension)
	if err != nil {
		return err
	}

	for _, file := range files {
		if err = s.check(file.name, string(file.contents)); err != nil {
			return fmt.Errorf("%s: %w", file.name, err)
		}
	}

	s.mu.Lock()
	custom := len(s.custom)
	s.mu.Unlock()

	// A loader of the application may hold templates that cannot be listed.
	if len(files) == 0 && custom == 0 && s.userLoader == nil {
		return ErrNoTemplates
	}

	s.cache.reset()
	return nil
}

// exists reports whether jet can find a template under the name, with one
// of the extensions it tries.
func (s *JetEngine) exists(name string) bool {
	name = path.Join("/", filepath.ToSlash(name))
	loader := jetLoader{engine: s}

	for _, ext := range [...]string{"", ".jet", ".html.jet", ".jet.html"} {
		if loader.Exists(name + ext) {
			return true
		}
	}

	return false
}

type (
	// JetRuntimeVars is a type alias for `jet.VarMap`.
	// Can be used at `AddJetRuntimeVars/JetEngine.AddRuntimeVars`
	// to set a runtime variable ${name} to the executing template.
	JetRuntimeVars = jet.VarMap

	// JetRuntime is a type alias of `jet.Runtime`,
	// can be used on RuntimeVariable input function.
	JetRuntime = jet.Runtime
)

// JetRuntimeVarsContextKey is the Iris Context key to keep any custom jet runtime variables.
// See `AddJetRuntimeVars` package-level function and `JetEngine.AddRuntimeVars` method.
const JetRuntimeVarsContextKey = "iris.jetvarmap"

// AddJetRuntimeVars sets or inserts runtime jet variables through the Iris Context.
// This gives the ability to add runtime variables from different handlers in the request chain,
// something that the jet template parser does not offer at all.
//
// Usage: views.AddJetRuntimeVars(ctx, views.JetRuntimeVars{...}).
// See `JetEngine.AddRuntimeVars` too.
func AddJetRuntimeVars(ctx *context.Context, jetVarMap JetRuntimeVars) {
	if v := ctx.Values().Get(JetRuntimeVarsContextKey); v != nil {
		if vars, ok := v.(JetRuntimeVars); ok {
			for key, value := range jetVarMap {
				vars[key] = value
			}
			return
		}
	}

	// A copy: the next call for this request writes into the stored map,
	// and the map of the caller may be one that every request is given.
	ctx.Values().Set(JetRuntimeVarsContextKey, maps.Clone(jetVarMap))
}

// AddRuntimeVars sets or inserts runtime jet variables through the Iris Context.
// This gives the ability to add runtime variables from different handlers in the request chain,
// something that the jet template parser does not offer at all.
//
// Usage: engine.AddRuntimeVars(ctx, views.JetRuntimeVars{...}).
// See `views.AddJetRuntimeVars` if package-level access is more meanful to the code flow.
func (s *JetEngine) AddRuntimeVars(ctx *context.Context, vars JetRuntimeVars) {
	AddJetRuntimeVars(ctx, vars)
}

// ExecuteTemplate should execute a template by its filename with an optional layout and bindingData.
// The layout is ignored: a Jet template names its own parent with
// {{ extends }}.
//
// The engine loads itself first if it was never loaded.
// It returns a view.ErrTemplateNotFound for a template that does not exist.
func (s *JetEngine) ExecuteTemplate(w io.Writer, filename string, layout string, bindingData any) error {
	// parses the templates when they were never loaded, and
	// re-parses them if reload is enabled.
	if err := s.loader.ready(s.developmentMode, s.build); err != nil {
		return err
	}

	// jet gives back the panic of a function when it is a runtime error,
	// or anything but an error.
	return protect(func() error {
		return s.execute(w, filename, bindingData)
	})
}

func (s *JetEngine) execute(w io.Writer, filename string, bindingData any) error {
	tmpl, err := s.set().GetTemplate(filename)
	if err != nil {
		if !s.exists(filename) {
			return ErrTemplateNotFound{Name: filename, IsLayout: false, Data: bindingData}
		}

		return err
	}

	// The variables of the render, in a map of its own: the maps they come
	// from may be given to every request.
	var vars JetRuntimeVars

	if ctx, ok := w.(*context.Context); ok {
		if runtimeVars, ok := ctx.Values().Get(JetRuntimeVarsContextKey).(JetRuntimeVars); ok {
			vars = maps.Clone(runtimeVars)
		}

		if m, ok := view.GetData(ctx).(context.Map); ok { // fix #1876
			if vars == nil {
				vars = make(JetRuntimeVars, len(m))
			}

			for k, v := range m {
				val, ok := v.(reflect.Value)
				if !ok {
					val = reflect.ValueOf(v)
				}
				vars[k] = val
			}
		}

		if v := ctx.Values().Get(s.jetDataContextKey); v != nil {
			if bindingData == nil {
				// if bindingData is nil, try to fill them by context key (a middleware can set data).
				bindingData = v
			} else if m, ok := bindingData.(context.Map); ok {
				// else if bindingData are passed to view.Render
				// and it's map try to fill with the new values passed from a middleware.
				// Into a copy: the data may be a map that every request is given.
				if mv, ok := v.(context.Map); ok {
					merged := make(context.Map, len(m)+len(mv))
					maps.Copy(merged, m)
					maps.Copy(merged, mv)
					bindingData = merged
				}
			}
		}
	}

	if bindingData == nil {
		return tmpl.Execute(w, vars, nil)
	}

	if vars == nil {
		vars = make(JetRuntimeVars)
	}

	return tmpl.Execute(w, vars, bindingData)
}
