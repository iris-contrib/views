package views

import (
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/kataras/iris/v14/middleware/view"

	"github.com/mailgun/raymond/v2"
)

// HandlebarsEngine contains the handlebars view engine structure.
type HandlebarsEngine struct {
	fs fs.FS
	// files configuration
	rootDir   string
	extension string
	reload    atomic.Bool // if true, each time the ExecuteTemplate is called the templates will be reloaded.
	// parser configuration
	layout string

	// mu guards funcs and custom. A request can add a helper, through
	// view.AddFunc, while another one is loading the engine.
	mu     sync.RWMutex
	funcs  template.FuncMap
	custom []handlebarsTmp // the templates of ParseTemplate, parsed again on every load.

	loader    loader
	templates atomic.Pointer[handlebarsSet] // the result of the last load that succeeded.
}

type handlebarsTmp struct {
	name     string
	contents string
	funcs    template.FuncMap
}

// handlebarsSet is one finished parse of the templates of an engine.
type handlebarsSet struct {
	ext       string
	templates map[string]*raymond.Template
}

func (set *handlebarsSet) resolve(name string) (*raymond.Template, bool) {
	name, ok := resolveName(name, set.ext, func(name string) bool {
		_, ok := set.templates[name]
		return ok
	})

	return set.templates[name], ok
}

var (
	_ view.Engine         = (*HandlebarsEngine)(nil)
	_ view.EngineFuncer   = (*HandlebarsEngine)(nil)
	_ view.EngineExtenser = (*HandlebarsEngine)(nil)
)

// Handlebars creates and returns a new handlebars view engine.
// The given "extension" MUST begin with a dot.
//
// Usage:
// Handlebars("./views", ".html") or
// Handlebars(embed.FS, ".html").RootDir("views") for embedded templates.
func Handlebars(fs any, extension string) *HandlebarsEngine {
	s := &HandlebarsEngine{
		fs:        getFS(fs),
		rootDir:   "/",
		extension: extension,
		funcs:     make(template.FuncMap), // for the templates of this engine.
	}

	return s
}

// RootDir sets the directory to be used as a starting point
// to load templates from the provided file system.
// It is what points the engine at a directory of an embed.FS.
//
// It panics if root is not a valid path inside the file system.
func (s *HandlebarsEngine) RootDir(root string) *HandlebarsEngine {
	root = filepath.ToSlash(root)
	if root != s.rootDir {
		s.fs = subFS(s.fs, root)
	}

	s.rootDir = root
	return s
}

// Name returns the handlebars engine's name.
func (s *HandlebarsEngine) Name() string {
	return "Handlebars"
}

// Ext returns the file extension which this view engine is responsible to render.
// If the filename extension on ExecuteTemplate is empty then this is appended.
func (s *HandlebarsEngine) Ext() string {
	return s.extension
}

// Reload if set to true the templates are reloading on each render,
// use it when you're in development and you don't want to restart
// the whole app when you edit a template file.
//
// Every render then parses all templates first, one render at a time.
// Do not use it in production.
func (s *HandlebarsEngine) Reload(developmentMode bool) *HandlebarsEngine {
	s.reload.Store(developmentMode)
	return s
}

// Layout sets the layout template file, which prints the page where it
// says {{ yield }} and may use {{ render "name" . }} to render other
// template files like headers and footers.
//
// The page reaches the layout as a "yield" entry of the template data, so a
// render through a layout needs the data to be a map[string]any or nil.
func (s *HandlebarsEngine) Layout(layoutFile string) *HandlebarsEngine {
	s.layout = layoutFile
	return s
}

// AddFunc adds a helper to the templates of this engine.
// It is legal to overwrite elements of the default actions:
// - render func(fullPartialName string, binding any) raymond.SafeString.
//
// A helper is given to a template when it is parsed, so it takes effect at
// the next load: add it before Load, or before the first render of an
// engine that loads itself.
func (s *HandlebarsEngine) AddFunc(funcName string, funcBody any) {
	s.mu.Lock()
	s.funcs[funcName] = funcBody
	s.mu.Unlock()
}

// AddGlobalFunc registers a global template function for all Handlebars view engines.
// A name that is registered already is replaced.
//
// It panics, as raymond does, if funcBody is not a function with one result.
func (s *HandlebarsEngine) AddGlobalFunc(funcName string, funcBody any) {
	// Checked here, before the helper of that name is taken out: raymond
	// would refuse it only after.
	if fn := reflect.TypeOf(funcBody); fn == nil || fn.Kind() != reflect.Func || fn.NumOut() != 1 {
		panic(fmt.Errorf("handlebars: the helper %s must be a function with one result but it is a %T", funcName, funcBody))
	}

	handlebarsRegistry.Lock()
	defer handlebarsRegistry.Unlock()

	// raymond panics for a name it knows already.
	raymond.RemoveHelper(funcName)
	raymond.RegisterHelper(funcName, funcBody)
}

// handlebarsRegistry serializes the registrations this package makes in the
// process-wide registry of raymond.
var handlebarsRegistry sync.Mutex

// Load parses the templates to the engine.
//
// Calling it is optional: an engine that was never loaded loads itself on
// its first ExecuteTemplate. Call it at startup to find a template that does
// not parse before the first request does.
//
// It may be called again to parse everything anew. If it fails then, the
// templates of the previous load keep rendering.
//
// Returns an error if something bad happens, user is responsible to catch it.
func (s *HandlebarsEngine) Load() error {
	return s.loader.load(s.build)
}

// IsLoaded reports whether the templates are loaded at least once or not.
func (s *HandlebarsEngine) IsLoaded() bool {
	return s.loader.loaded()
}

// ParseTemplate adds a custom template from text.
// The funcs are helpers for this template alone.
//
// A template given here is kept, and parsed again on every load. When it
// returns an error, nothing was added.
func (s *HandlebarsEngine) ParseTemplate(name string, contents string, funcs template.FuncMap) error {
	tmpl := handlebarsTmp{name: strings.TrimPrefix(name, "/"), contents: contents, funcs: funcs}

	return s.loader.add(func(attempted bool) error {
		s.mu.RLock()
		custom := upsert(s.custom, tmpl, func(c handlebarsTmp) bool { return c.name == tmpl.name })
		s.mu.RUnlock()

		if attempted {
			// Loaded already: build with it, so that it renders at once.
			if err := s.buildWith(custom); err != nil {
				return err
			}
		} else if _, err := s.parse([]handlebarsTmp{tmpl}); err != nil {
			// Not loaded yet: parse it now, so that a template that does
			// not parse is refused here and not by the first render.
			return err
		}

		s.mu.Lock()
		s.custom = custom
		s.mu.Unlock()
		return nil
	})
}

// build parses every template and, when all of them parse, publishes the
// result. The loader calls it, one call at a time.
func (s *HandlebarsEngine) build() error {
	s.mu.RLock()
	custom := slices.Clone(s.custom)
	s.mu.RUnlock()

	return s.buildWith(custom)
}

// buildWith is build with the given templates as the ones of ParseTemplate.
func (s *HandlebarsEngine) buildWith(sources []handlebarsTmp) error {
	files, err := readTemplates(s.fs, s.extension)
	if err != nil {
		return err
	}

	for _, file := range files {
		sources = append(sources, handlebarsTmp{name: file.name, contents: string(file.contents)})
	}

	set, err := s.parse(sources)
	if err != nil {
		return err
	}

	if len(set.templates) == 0 {
		return ErrNoTemplates
	}

	s.templates.Store(set)
	return nil
}

// parse parses the given templates into a new set.
func (s *HandlebarsEngine) parse(sources []handlebarsTmp) (*handlebarsSet, error) {
	s.mu.RLock()
	funcs := maps.Clone(s.funcs)
	s.mu.RUnlock()

	set := &handlebarsSet{
		ext:       s.extension,
		templates: make(map[string]*raymond.Template, len(sources)),
	}

	for _, source := range sources {
		tmpl, err := raymond.Parse(source.contents)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", source.name, err)
		}

		// The helpers belong to the template and not to the process-wide
		// registry of raymond, which is what lets two engines live in one
		// program. One map, so that a name is registered once: raymond
		// panics on the second.
		helpers := map[string]any{"render": set.render}
		maps.Copy(helpers, funcs)
		maps.Copy(helpers, source.funcs)
		tmpl.RegisterHelpers(helpers)

		set.templates[source.name] = tmpl
	}

	return set, nil
}

// handlebarsDepth is the entry of the private data of raymond that counts
// how deep the render helper is.
const handlebarsDepth = "views.render.depth"

// render is the helper behind {{ render "name" . }}.
// raymond turns its panic into the error of the render.
func (set *handlebarsSet) render(partial string, binding any, options *raymond.Options) raymond.SafeString {
	tmpl, ok := set.resolve(partial)
	if !ok {
		panic(ErrTemplateNotFound{Name: partial, Data: binding})
	}

	// A template that renders itself would overflow the stack, which Go
	// cannot recover from. The depth travels in the private data of the
	// execution, so it belongs to this render alone.
	depth, _ := options.Data(handlebarsDepth).(int)
	if depth >= maxTemplateNesting {
		panic(fmt.Errorf("render %s: more than %d templates deep, does a template render itself?", partial, maxTemplateNesting))
	}

	frame := options.NewDataFrame()
	frame.Set(handlebarsDepth, depth+1)

	contents, err := tmpl.ExecWith(binding, frame)
	if err != nil {
		panic(fmt.Errorf("render %s: %w", partial, err))
	}

	return raymond.SafeString(contents)
}

// ExecuteTemplate executes a template and writes its result to the w writer.
// The layout is the layout argument or, when that is empty, the one set by
// Layout. NoLayout renders the template without any. A render through a
// layout needs the data to be a map[string]any or nil.
//
// The engine loads itself first if it was never loaded.
// It returns a view.ErrTemplateNotFound for a template or a layout that
// does not exist. Nothing is written to w when the execution fails.
func (s *HandlebarsEngine) ExecuteTemplate(w io.Writer, filename string, layout string, bindingData any) error {
	// parses the templates when they were never loaded, and
	// re-parses them if reload is enabled.
	if err := s.loader.ready(s.reload.Load(), s.build); err != nil {
		return err
	}

	set := s.templates.Load()

	tmpl, ok := set.resolve(filename)
	if !ok {
		return ErrTemplateNotFound{Name: filename, IsLayout: false, Data: bindingData}
	}

	binding := bindingData

	if layout = getLayout(layout, s.layout); layout != "" {
		layoutTmpl, ok := set.resolve(layout)
		if !ok {
			return ErrTemplateNotFound{Name: layout, IsLayout: true, Data: bindingData}
		}

		// The page goes to the layout as one more entry of the data. The
		// entry is added to a copy: the map may be one that every request
		// shares, as the one of view.WithData is.
		var data map[string]any

		switch m := bindingData.(type) {
		case nil:
			data = make(map[string]any, 1)
		case map[string]any: // handlebars accepts maps,
			data = make(map[string]any, len(m)+1)
			maps.Copy(data, m)
		default:
			return fmt.Errorf("handlebars: a layout needs the template data to be a map[string]any but it is a %T", bindingData)
		}

		contents, err := execHandlebars(tmpl, bindingData)
		if err != nil {
			return err
		}

		data["yield"] = raymond.SafeString(contents)
		tmpl, binding = layoutTmpl, data
	}

	res, err := execHandlebars(tmpl, binding)
	if err != nil {
		return err
	}

	_, err = io.WriteString(w, res)
	return err
}

// execHandlebars executes a template. raymond gives back the panic of a
// helper when it is a runtime error, such as an index out of range.
func execHandlebars(tmpl *raymond.Template, binding any) (res string, err error) {
	err = protect(func() error {
		var err error
		res, err = tmpl.Exec(binding)
		return err
	})

	return
}
