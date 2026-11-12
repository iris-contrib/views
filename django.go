package views

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"maps"
	stdPath "path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/kataras/iris/v14/middleware/view"

	"github.com/fatih/structs"
	"github.com/flosch/pongo2/v4"
)

type (
	// Value type alias for pongo2.Value
	Value = pongo2.Value
	// Error type alias for pongo2.Error
	Error = pongo2.Error
	// FilterFunction type alias for pongo2.FilterFunction
	FilterFunction = pongo2.FilterFunction

	// Parser type alias for pongo2.Parser
	Parser = pongo2.Parser
	// Token type alias for pongo2.Token
	Token = pongo2.Token
	// INodeTag type alias for pongo2.InodeTag
	INodeTag = pongo2.INodeTag
	// TagParser the function signature of the tag's parser you will have
	// to implement in order to create a new tag.
	//
	// 'doc' is providing access to the whole document while 'arguments'
	// is providing access to the user's arguments to the tag:
	//
	//     {% your_tag_name some "arguments" 123 %}
	//
	// start_token will be the *Token with the tag's name in it (here: your_tag_name).
	//
	// Please see the Parser documentation on how to use the parser.
	// See `RegisterTag` for more information about writing a tag as well.
	TagParser = pongo2.TagParser
)

// AsValue converts any given value to a pongo2.Value
// Usually being used within own functions passed to a template
// through a Context or within filter functions.
//
// Example:
//
//	AsValue("my string")
//
// Shortcut for `pongo2.AsValue`.
var AsValue = pongo2.AsValue

// AsSafeValue works like AsValue, but does not apply the 'escape' filter.
// Shortcut for `pongo2.AsSafeValue`.
var AsSafeValue = pongo2.AsSafeValue

// tDjangoAssetLoader reads the templates that other templates name with
// "extends" and "include". Every name is relative to the root of the
// engine's file system, whichever template it is written in.
type tDjangoAssetLoader struct {
	engine *DjangoEngine

	// reads counts the templates read for the one that is being parsed,
	// while limited is set. pongo2 parses a template that includes or
	// extends itself until the stack runs out.
	limited atomic.Bool
	reads   atomic.Int64
}

// Abs calculates the path to a given template. Whenever a path must be resolved
// due to an import from another template, the base equals the parent template's path.
func (l *tDjangoAssetLoader) Abs(base, name string) string {
	return stdPath.Join("/", name)
}

// Get returns an io.Reader where the template's content can be read from.
func (l *tDjangoAssetLoader) Get(path string) (io.Reader, error) {
	if l.limited.Load() && l.reads.Add(1) > maxTemplateReads {
		return nil, errTooManyReads
	}

	res, err := asset(l.engine.fs, path)
	if err != nil {
		return nil, err
	}

	return bytes.NewReader(res), nil
}

// DjangoEngine contains the django view engine structure.
type DjangoEngine struct {
	fs fs.FS
	// files configuration
	rootDir   string
	extension string
	reload    atomic.Bool

	// mu guards globals and custom. A request can add a global, through
	// view.AddFunc, while other requests render.
	mu sync.RWMutex
	// globals share context fields between templates.
	globals map[string]any
	custom  []customTmp // the templates of ParseTemplate, parsed again on every load.

	loader    loader
	templates atomic.Pointer[map[string]*pongo2.Template] // the result of the last load that succeeded.

	// Set is the pongo2 template set of the engine.
	// It is nil until the first Load, ParseTemplate or render, unless the
	// application assigns one of its own before that.
	Set     *pongo2.TemplateSet
	setOnce sync.Once
	assets  tDjangoAssetLoader
}

var (
	_ view.Engine         = (*DjangoEngine)(nil)
	_ view.EngineFuncer   = (*DjangoEngine)(nil)
	_ view.EngineExtenser = (*DjangoEngine)(nil)
)

// Django creates and returns a new django view engine.
// The given "extension" MUST begin with a dot.
//
// A Django template names its own parent with {% extends %}, so the layout
// argument of ExecuteTemplate is ignored by this engine.
//
// Usage:
// Django("./views", ".html") or
// Django(embed.FS, ".html").RootDir("views") for embedded templates.
func Django(fs any, extension string) *DjangoEngine {
	s := &DjangoEngine{
		fs:        getFS(fs),
		rootDir:   "/",
		extension: extension,
		globals:   make(map[string]any),
	}

	return s
}

// RootDir sets the directory to be used as a starting point
// to load templates from the provided file system.
// It is what points the engine at a directory of an embed.FS.
//
// It panics if root is not a valid path inside the file system.
func (s *DjangoEngine) RootDir(root string) *DjangoEngine {
	root = filepath.ToSlash(root)
	if root != s.rootDir {
		s.fs = subFS(s.fs, root)
	}

	s.rootDir = root
	return s
}

// Name returns the django engine's name.
func (s *DjangoEngine) Name() string {
	return "Django"
}

// Ext returns the file extension which this view engine is responsible to render.
// If the filename extension on ExecuteTemplate is empty then this is appended.
func (s *DjangoEngine) Ext() string {
	return s.extension
}

// Reload if set to true the templates are reloading on each render,
// use it when you're in development and you don't want to restart
// the whole app when you edit a template file.
//
// Every render then parses all templates first, one render at a time.
// Do not use it in production.
func (s *DjangoEngine) Reload(developmentMode bool) *DjangoEngine {
	s.reload.Store(developmentMode)
	return s
}

// AddFunc adds the function to the template's Globals.
// A template calls it as {{ name(argument) }}.
// Unlike the other engines, it takes effect at once, loaded or not.
func (s *DjangoEngine) AddFunc(funcName string, funcBody any) {
	s.mu.Lock()
	s.globals[funcName] = funcBody
	s.mu.Unlock()
}

// AddFilter registers a new filter.
//
// Same as `RegisterFilter`.
func (s *DjangoEngine) AddFilter(filterName string, filterBody FilterFunction) *DjangoEngine {
	return s.registerFilter(filterName, filterBody)
}

// RegisterFilter registers a new filter.
//
// pongo2 keeps its filters in one registry for the whole process. A filter
// is therefore shared by every Django engine of the program, and a name
// that is registered again replaces the filter for all of them. Register
// filters at startup, before anything renders: the registry is not safe to
// change while templates execute.
//
// See http://www.florian-schlachter.de/post/pongo2/ for more about
// writing filters and tags.
func (s *DjangoEngine) RegisterFilter(filterName string, filterBody FilterFunction) *DjangoEngine {
	return s.registerFilter(filterName, filterBody)
}

// djangoRegistry serializes the registrations this package makes in the
// process-wide registry of pongo2.
var djangoRegistry sync.Mutex

func (s *DjangoEngine) registerFilter(filterName string, fn FilterFunction) *DjangoEngine {
	djangoRegistry.Lock()
	defer djangoRegistry.Unlock()

	// Neither call can fail after the check.
	if pongo2.FilterExists(filterName) {
		_ = pongo2.ReplaceFilter(filterName, fn)
	} else {
		_ = pongo2.RegisterFilter(filterName, fn)
	}

	return s
}

// RegisterTag registers a new tag. You usually want to call this
// function in the tag's init() function:
// http://golang.org/doc/effective_go.html#init
//
// Tags live in the process-wide registry of pongo2, like filters, and it
// returns an error for a name that is already registered.
//
// See http://www.florian-schlachter.de/post/pongo2/ for more about
// writing filters and tags.
func (s *DjangoEngine) RegisterTag(tagName string, fn TagParser) error {
	djangoRegistry.Lock()
	defer djangoRegistry.Unlock()

	return pongo2.RegisterTag(tagName, fn)
}

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
func (s *DjangoEngine) Load() error {
	return s.loader.load(s.build)
}

// IsLoaded reports whether the templates are loaded at least once or not.
func (s *DjangoEngine) IsLoaded() bool {
	return s.loader.loaded()
}

// ParseTemplate adds a custom template from text.
// This parser does not support funcs per template. Use the `AddFunc` instead.
//
// A template given here is kept, and parsed again on every load. When it
// returns an error, nothing was added.
func (s *DjangoEngine) ParseTemplate(name string, contents []byte) error {
	tmpl := customTmp{name: strings.TrimPrefix(name, "/"), contents: contents}

	return s.loader.add(func(attempted bool) error {
		s.mu.RLock()
		custom := upsert(s.custom, tmpl, func(c customTmp) bool { return c.name == tmpl.name })
		s.mu.RUnlock()

		if attempted {
			// Loaded already: build with it, so that it renders at once.
			if err := s.buildWith(custom); err != nil {
				return err
			}
		} else if _, err := s.fromBytes(contents); err != nil {
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

// set returns the pongo2 set, which is created on first use: the file
// system it reads from is final only after the engine is configured.
func (s *DjangoEngine) set() *pongo2.TemplateSet {
	s.setOnce.Do(func() {
		s.assets.engine = s

		// The application may have assigned its own.
		if s.Set == nil {
			s.Set = pongo2.NewSet("", &s.assets)
		}
	})

	return s.Set
}

// fromBytes parses one template. The loader calls it and ParseTemplate, one
// call at a time.
func (s *DjangoEngine) fromBytes(contents []byte) (*pongo2.Template, error) {
	set := s.set()

	s.assets.reads.Store(0)
	s.assets.limited.Store(true)
	defer s.assets.limited.Store(false)

	tmpl, err := set.FromBytes(contents)
	if s.assets.reads.Load() > maxTemplateReads {
		// The error of pongo2 is as deep as the recursion was.
		return nil, errTooManyReads
	}

	return tmpl, err
}

// build parses every template and, when all of them parse, publishes the
// result. The loader calls it, one call at a time.
func (s *DjangoEngine) build() error {
	s.mu.RLock()
	custom := slices.Clone(s.custom)
	s.mu.RUnlock()

	return s.buildWith(custom)
}

// buildWith is build with the given templates as the ones of ParseTemplate.
func (s *DjangoEngine) buildWith(custom []customTmp) error {
	files, err := readTemplates(s.fs, s.extension)
	if err != nil {
		return err
	}

	templates := make(map[string]*pongo2.Template, len(custom)+len(files))

	for _, tmpl := range custom {
		if templates[tmpl.name], err = s.fromBytes(tmpl.contents); err != nil {
			return fmt.Errorf("%s: %w", tmpl.name, err)
		}
	}

	for _, file := range files {
		if templates[file.name], err = s.fromBytes(file.contents); err != nil {
			return fmt.Errorf("%s: %w", file.name, err)
		}
	}

	if len(templates) == 0 {
		return ErrNoTemplates
	}

	s.templates.Store(&templates)
	return nil
}

// getPongoContext returns the pongo2.Context from map[string]any or from pongo2.Context, used internaly
func getPongoContext(templateData any) (pongo2.Context, error) {
	if templateData == nil {
		return nil, nil
	}

	switch data := templateData.(type) {
	case pongo2.Context:
		return data, nil
	case map[string]any:
		return pongo2.Context(data), nil
	default:
		// if struct, convert it to map[string]any
		if structs.IsStruct(data) {
			return pongo2.Context(structs.Map(data)), nil
		}

		return nil, fmt.Errorf("django: template data should be a map[string]any or a struct but it is a %T", templateData)
	}
}

// context returns what a template executes with: the globals of the
// engine, and over them the data of the render.
func (s *DjangoEngine) context(bindingData any) (pongo2.Context, error) {
	data, err := getPongoContext(bindingData)
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.globals) == 0 {
		return data, nil
	}

	// A new map on every render: the data may be a map that every request
	// shares, as the one of view.WithData is.
	ctx := make(pongo2.Context, len(s.globals)+len(data))
	maps.Copy(ctx, s.globals)
	maps.Copy(ctx, data)
	return ctx, nil
}

// ExecuteTemplate executes a template and writes its result to the w writer.
// The layout is ignored: a Django template names its own parent with
// {% extends %}.
//
// The engine loads itself first if it was never loaded.
// It returns a view.ErrTemplateNotFound for a template that does not
// exist. Nothing is written to w when the execution fails.
func (s *DjangoEngine) ExecuteTemplate(w io.Writer, filename string, _ string, bindingData any) error {
	// parses the templates when they were never loaded, and
	// re-parses them if reload is enabled.
	if err := s.loader.ready(s.reload.Load(), s.build); err != nil {
		return err
	}

	templates := *s.templates.Load()

	name, ok := resolveName(filename, s.extension, func(name string) bool {
		_, ok := templates[name]
		return ok
	})
	if !ok {
		return ErrTemplateNotFound{Name: filename, IsLayout: false, Data: bindingData}
	}

	ctx, err := s.context(bindingData)
	if err != nil {
		return err
	}

	// pongo2 does not recover from a function that panics.
	return protect(func() error {
		return templates[name].ExecuteWriter(ctx, w)
	})
}
