package views

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"maps"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/kataras/iris/v14/middleware/view"
)

// HTMLEngine contains the html view engine structure.
// Read more at: https://pkg.go.dev/text/template#hdr-Functions.
//
// It is also the engine behind Pug and Ace, which turn their own syntax into
// html/template text before it is parsed.
type HTMLEngine struct {
	name string // the view engine's name, can be HTML, Ace or Pug.
	// the file system to load from.
	fs fs.FS
	// files configuration
	rootDir   string
	extension string
	// if true, each time the ExecuteTemplate is called the templates will be reloaded.
	reload atomic.Bool
	// parser configuration
	left   string
	right  string
	layout string

	// mu guards the fields below. A request can add a function, through
	// view.AddFunc, while another one is loading the engine.
	mu          sync.RWMutex
	options     []string // (text) template options
	layoutFuncs template.FuncMap
	funcs       template.FuncMap
	custom      []customTmp // the templates of ParseTemplate, parsed again on every load.

	// middleware turns the source of one template into html/template text.
	// Pug and Ace set it. funcs holds the name of every function a template
	// may call, for a parser that checks them.
	middleware func(name string, contents []byte, funcs template.FuncMap) (string, error)

	loader loader
	set    atomic.Pointer[htmlSet] // the templates of the last load that succeeded.

	// Templates is a copy of the template set of the last successful load,
	// nil before it. Use it to look templates up, for example with
	// DefinedTemplates. The engine does not render with it, so parsing into
	// it or changing its functions has no effect on what the engine renders.
	//
	// Every load replaces it, with no lock: read it when no load can run.
	// With Reload(true) every render is a load.
	Templates *template.Template
}

type customTmp struct {
	name     string
	contents []byte
	funcs    template.FuncMap
}

var (
	_ view.Engine         = (*HTMLEngine)(nil)
	_ view.EngineFuncer   = (*HTMLEngine)(nil)
	_ view.EngineExtenser = (*HTMLEngine)(nil)
)

// HTML creates and returns a new html view engine.
// The html engine used like the "html/template" standard go package
// but with a lot of extra features.
// The given "extension" MUST begin with a dot.
//
// Usage:
// HTML("./views", ".html") or
// HTML(embed.FS, ".html").RootDir("views") for embedded templates or
// HTML(nil, ".html") and ParseTemplate("hello.html", []byte("hello {{.Name}}"), nil)
// for templates given as text.
func HTML(dirOrFS any, extension string) *HTMLEngine {
	s := &HTMLEngine{
		name:        "HTML",
		fs:          getFS(dirOrFS),
		rootDir:     "/",
		extension:   extension,
		left:        "{{",
		right:       "}}",
		layout:      "",
		layoutFuncs: make(template.FuncMap),
		funcs:       make(template.FuncMap),
	}

	return s
}

// RootDir sets the directory to be used as a starting point
// to load templates from the provided file system.
// It is what points the engine at a directory of an embed.FS.
//
// It panics if root is not a valid path inside the file system.
func (s *HTMLEngine) RootDir(root string) *HTMLEngine {
	root = filepath.ToSlash(root)
	if root != s.rootDir {
		s.fs = subFS(s.fs, root)
	}

	s.rootDir = root
	return s
}

// FS sets the directory or the file system the templates are loaded from,
// replacing the one given to the constructor.
func (s *HTMLEngine) FS(dirOrFS any) *HTMLEngine {
	s.fs = getFS(dirOrFS)
	return s
}

// Name returns the engine's name.
func (s *HTMLEngine) Name() string {
	return s.name
}

// Ext returns the file extension which this view engine is responsible to render.
// If the filename extension on ExecuteTemplate is empty then this is appended.
func (s *HTMLEngine) Ext() string {
	return s.extension
}

// Reload if set to true the templates are reloading on each render,
// use it when you're in development and you don't want to restart
// the whole app when you edit a template file.
//
// Every render then parses all templates first, one render at a time.
// Do not use it in production.
func (s *HTMLEngine) Reload(developmentMode bool) *HTMLEngine {
	s.reload.Store(developmentMode)
	return s
}

// Option sets options for the template. Options are described by
// strings, either a simple string or "key=value". There can be at
// most one equals sign in an option string. An option string that is
// unrecognized or otherwise invalid makes Load return an error.
//
// Known options:
//
// missingkey: Control the behavior during execution if a map is
// indexed with a key that is not present in the map.
//
//	"missingkey=default" or "missingkey=invalid"
//		The default behavior: Do nothing and continue execution.
//		If printed, the result of the index operation is the string
//		"<no value>".
//	"missingkey=zero"
//		The operation returns the zero value for the map type's element.
//	"missingkey=error"
//		Execution stops immediately with an error.
func (s *HTMLEngine) Option(opt ...string) *HTMLEngine {
	s.mu.Lock()
	s.options = append(s.options, opt...)
	s.mu.Unlock()
	return s
}

// Delims sets the action delimiters to the specified strings, to be used in
// templates. An empty delimiter stands for the
// corresponding default: {{ or }}.
func (s *HTMLEngine) Delims(left, right string) *HTMLEngine {
	s.left, s.right = left, right
	return s
}

// Layout sets the layout template file which inside should use
// the {{ yield . }} func to yield the main template file
// and optionally {{partial/partial_r/render . }} to render other template files like headers and footers
//
// The 'tmplLayoutFile' is a relative path of the templates base directory,
// for the template file with its extension.
//
// Example: HTML("./templates", ".html").Layout("layouts/mainLayout.html")
//
//	// mainLayout.html is inside: "./templates/layouts/".
//
// Note: Layout can be changed for a specific call with the layout argument
// of view.Render, or with view.Layout and view.WithLayout.
// Pass NoLayout there to render a template without this layout.
func (s *HTMLEngine) Layout(layoutFile string) *HTMLEngine {
	s.layout = layoutFile
	return s
}

// AddLayoutFunc adds the function to the template's function map.
// It is legal to overwrite elements of the default layout actions:
// - current  func() string
// - partial func(partialName string, binding any) (template.HTML, error)
// - partial_r func(partialName string, binding any) (template.HTML, error)
// - part func(partName string, binding any) (template.HTML, error)
// - render func(fullPartialName string, binding any) (template.HTML, error).
//
// The function takes effect at the next load. Functions belong to the whole
// template set in html/template, so pages can call it too, and a function of
// the same name added with AddFunc is replaced by it. The yield action
// always renders the current page and cannot be replaced.
func (s *HTMLEngine) AddLayoutFunc(funcName string, funcBody any) *HTMLEngine {
	s.mu.Lock()
	s.layoutFuncs[funcName] = funcBody
	s.mu.Unlock()
	return s
}

// AddFunc adds the function to the template's function map.
// It is legal to overwrite elements of the default actions:
// - render func(fullPartialName string, binding any) (template.HTML, error).
//
// html/template resolves function names when it parses, so the function
// takes effect at the next load: add it before Load, or before the first
// render of an engine that loads itself.
func (s *HTMLEngine) AddFunc(funcName string, funcBody any) {
	s.mu.Lock()
	s.funcs[funcName] = funcBody
	s.mu.Unlock()
}

// SetFuncs overrides the template funcs with the given "funcMap".
func (s *HTMLEngine) SetFuncs(funcMap template.FuncMap) *HTMLEngine {
	if funcMap == nil {
		funcMap = make(template.FuncMap)
	}

	s.mu.Lock()
	s.funcs = funcMap
	s.mu.Unlock()

	return s
}

// Funcs adds the elements of the argument map to the template's function map.
// It is legal to overwrite elements of the map. The return
// value is the template, so calls can be chained.
func (s *HTMLEngine) Funcs(funcMap template.FuncMap) *HTMLEngine {
	s.mu.Lock()
	maps.Copy(s.funcs, funcMap)
	s.mu.Unlock()

	return s
}

// Load parses the templates to the engine.
// It's also responsible to add the necessary global functions.
//
// Calling it is optional: an engine that was never loaded loads itself on
// its first ExecuteTemplate. Call it at startup to find a template that does
// not parse before the first request does.
//
// It may be called again to parse everything anew. If it fails then, the
// templates of the previous load keep rendering.
//
// Returns an error if something bad happens, caller is responsible to handle that.
func (s *HTMLEngine) Load() error {
	return s.loader.load(s.build)
}

// IsLoaded reports whether the templates are loaded at least once or not.
func (s *HTMLEngine) IsLoaded() bool {
	return s.loader.loaded()
}

// ParseTemplate adds a custom template to the root template.
// The funcs are for this template only as far as parsing goes: html/template
// keeps one function map for the whole set.
//
// A template given here is kept, and parsed again on every load. When it
// returns an error, nothing was added.
func (s *HTMLEngine) ParseTemplate(name string, contents []byte, funcs template.FuncMap) (err error) {
	tmpl := customTmp{
		name:     strings.TrimPrefix(name, "/"),
		contents: contents,
		funcs:    funcs,
	}

	return s.loader.add(func(attempted bool) error {
		s.mu.RLock()
		custom := upsert(s.custom, tmpl, func(c customTmp) bool { return c.name == tmpl.name })
		s.mu.RUnlock()

		if attempted {
			// Loaded already: build with it, so that it renders at once.
			if err := s.buildWith(custom); err != nil {
				return err
			}
		} else if _, err := s.parse(custom, nil); err != nil {
			// Not loaded yet: parse it now, so that a template that does
			// not parse is refused here and not by the first render. The
			// other templates given as text are parsed with it, because a
			// function given to one of them is known to all.
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
func (s *HTMLEngine) build() error {
	s.mu.RLock()
	custom := slices.Clone(s.custom)
	s.mu.RUnlock()

	return s.buildWith(custom)
}

// buildWith is build with the given templates as the ones of ParseTemplate.
func (s *HTMLEngine) buildWith(custom []customTmp) error {
	files, err := readTemplates(s.fs, s.extension)
	if err != nil {
		return err
	}

	set, err := s.parse(custom, files)
	if err != nil {
		return err
	}

	if len(set.names) == 0 {
		return ErrNoTemplates
	}

	// The first instance is made here and not by a request. Templates gets
	// one more, which no render uses: whatever a caller does with that
	// field cannot reach a render.
	first, err := set.newInstance()
	if err != nil {
		return err
	}
	set.free = append(set.free, first)

	exported, err := set.newInstance()
	if err != nil {
		return err
	}

	s.Templates = exported.root
	s.set.Store(set)
	return nil
}

// parse parses the given templates into a new set.
func (s *HTMLEngine) parse(custom []customTmp, files []templateFile) (*htmlSet, error) {
	s.mu.RLock()
	options := slices.Clone(s.options)
	funcs := make(template.FuncMap, len(s.funcs)+len(s.layoutFuncs))
	maps.Copy(funcs, s.funcs)
	maps.Copy(funcs, s.layoutFuncs)
	s.mu.RUnlock()

	set := &htmlSet{
		ext:        s.extension,
		names:      make(map[string]struct{}, len(custom)+len(files)),
		overridden: make(map[string]struct{}),
		maxIdle:    runtime.GOMAXPROCS(0),
	}

	// The built-in actions are bound to a render, so every instance of the
	// set gets its own. Here they only have to exist, for the parser.
	builtin := new(htmlInstance).funcs()
	override := func(funcs template.FuncMap) {
		for name := range funcs {
			if _, ok := builtin[name]; ok && name != "yield" {
				set.overridden[name] = struct{}{}
			}
		}
	}

	known := maps.Clone(builtin)
	maps.Copy(known, funcs)
	override(funcs)

	root := template.New(s.rootDir).Delims(s.left, s.right).Funcs(known)
	if len(options) > 0 {
		root.Option(options...)
	}

	parse := func(name string, contents []byte, extra template.FuncMap) error {
		text := string(contents)

		if s.middleware != nil {
			names := known
			if len(extra) > 0 {
				names = maps.Clone(known)
				maps.Copy(names, extra)
			}

			var err error
			if text, err = s.middleware(name, contents, names); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}

		tmpl := root.New(name)
		if len(extra) > 0 {
			tmpl.Funcs(extra)
			override(extra)
		}

		if _, err := tmpl.Parse(text); err != nil {
			return fmt.Errorf("%s: parse template: %w", name, err)
		}

		return nil
	}

	for _, tmpl := range custom {
		if err := parse(tmpl.name, tmpl.contents, tmpl.funcs); err != nil {
			return nil, err
		}
	}

	for _, file := range files {
		if err := parse(file.name, file.contents, nil); err != nil {
			return nil, err
		}
	}

	for _, tmpl := range root.Templates() {
		if tmpl.Tree != nil {
			set.names[tmpl.Name()] = struct{}{}
		}
	}

	set.master = root
	return set, nil
}

// htmlSet is one finished parse of the templates of an engine.
//
// html/template keeps one function map for a whole set, so a layout cannot
// learn which page it is rendering except through that map. Changing the map
// for every request, which this engine did up to Iris v12, lets two requests
// that render different pages through the same layout at the same moment
// swap pages. A set therefore renders through instances, each a clone of the
// set with its own functions, and an instance serves one render at a time.
type htmlSet struct {
	// master is parsed and never executed, because html/template refuses to
	// clone a set that has been executed.
	master *template.Template
	names  map[string]struct{}
	ext    string

	// overridden holds the built-in actions that the application replaced
	// with a function of its own.
	overridden map[string]struct{}

	// free holds the instances no render is using, up to maxIdle of them.
	// That is as many as can run at once when renders only compute. More
	// are made when renders wait inside a template function, for a query
	// for example. Those go to spare when they come back, where the garbage
	// collector may take them: a burst must not keep its memory for good.
	mu      sync.Mutex // guards free.
	free    []*htmlInstance
	maxIdle int
	spare   sync.Pool

	// grow is held while an instance is cloned. A clone costs about a
	// thousand times what a render does, so a burst of first requests waits
	// here and reuses the instances that come back, instead of each one
	// cloning the whole set for itself.
	grow sync.Mutex
}

func (set *htmlSet) has(name string) bool {
	_, ok := set.names[name]
	return ok
}

func (set *htmlSet) resolve(name string) (string, bool) {
	return resolveName(name, set.ext, set.has)
}

// get returns an instance that no other render is using.
func (set *htmlSet) get() (*htmlInstance, error) {
	if inst := set.pop(); inst != nil {
		return inst, nil
	}

	set.grow.Lock()
	defer set.grow.Unlock()

	if inst := set.pop(); inst != nil {
		return inst, nil
	}

	return set.newInstance()
}

func (set *htmlSet) pop() *htmlInstance {
	set.mu.Lock()
	if n := len(set.free); n > 0 {
		inst := set.free[n-1]
		set.free[n-1] = nil
		set.free = set.free[:n-1]
		set.mu.Unlock()
		return inst
	}
	set.mu.Unlock()

	inst, _ := set.spare.Get().(*htmlInstance)
	return inst
}

func (set *htmlSet) put(inst *htmlInstance) {
	set.mu.Lock()
	if len(set.free) < set.maxIdle {
		set.free = append(set.free, inst)
		set.mu.Unlock()
		return
	}
	set.mu.Unlock()

	set.spare.Put(inst)
}

func (set *htmlSet) newInstance() (*htmlInstance, error) {
	root, err := set.master.Clone()
	if err != nil {
		return nil, err
	}

	inst := &htmlInstance{set: set, root: root}

	funcs := inst.funcs()
	for name := range set.overridden {
		delete(funcs, name)
	}
	root.Funcs(funcs)

	return inst, nil
}

// maxTemplateNesting is how deep the built-in actions may nest. A partial
// that renders itself would otherwise overflow the stack, which Go cannot
// recover from.
const maxTemplateNesting = 100

// htmlInstance is a clone of a set that one render at a time executes in.
// Its built-in actions read the fields below, which is what binds them to
// that render.
type htmlInstance struct {
	set  *htmlSet
	root *template.Template

	page     string // the template the render was asked for.
	layout   bool   // the render goes through a layout, so yield has a page to render.
	yielding bool   // the page is being rendered by yield.
	depth    int
}

// exec renders a template of the instance into a string.
func (i *htmlInstance) exec(name string, binding any) (template.HTML, error) {
	if i.depth >= maxTemplateNesting {
		return "", fmt.Errorf("views: %s: more than %d templates deep, does a template render itself?", name, maxTemplateNesting)
	}

	i.depth++
	defer func() { i.depth-- }()

	buf := getBuffer()
	defer putBuffer(buf)

	err := i.root.ExecuteTemplate(buf, name, binding)
	// Return safe HTML here since we are rendering our own template.
	return template.HTML(buf.String()), err
}

// execIfExists renders a template that a page may or may not have.
func (i *htmlInstance) execIfExists(name string, binding any) (template.HTML, error) {
	if !i.set.has(name) {
		return "", nil
	}

	return i.exec(name, binding)
}

// funcs returns the built-in actions.
func (i *htmlInstance) funcs() template.FuncMap {
	return template.FuncMap{
		// yield renders the current page inside its layout. Anywhere else
		// it renders nothing.
		"yield": func(binding any) (template.HTML, error) {
			if !i.layout || i.yielding {
				return "", nil
			}

			i.yielding = true
			defer func() { i.yielding = false }()

			return i.exec(i.page, binding)
		},
		// current returns the name of the current page.
		"current": func() string {
			return i.page
		},
		// part renders the template "{page without extension}-{partName}",
		// which a page defines for its layout with {{ define }}.
		"part": func(partName string, binding any) (template.HTML, error) {
			page := strings.TrimSuffix(i.page, i.set.ext)
			return i.execIfExists(page+"-"+partName, binding)
		},
		// partial renders the template "{partialName}-{page}".
		"partial": func(partialName string, binding any) (template.HTML, error) {
			return i.execIfExists(partialName+"-"+i.page, binding)
		},
		// partial related to current page,
		// it would be easier for adding pages' style/script inline
		// for example when using partial_r '.script' in layout.html
		// templates/users/index.html would load templates/users/index.script.html
		"partial_r": func(partialName string, binding any) (template.HTML, error) {
			ext := path.Ext(i.page)
			return i.execIfExists(strings.TrimSuffix(i.page, ext)+partialName+ext, binding)
		},
		// render renders any template by its name.
		"render": func(fullPartialName string, binding any) (template.HTML, error) {
			name, ok := i.set.resolve(fullPartialName)
			if !ok {
				return "", ErrTemplateNotFound{Name: fullPartialName, Data: binding}
			}

			return i.exec(name, binding)
		},
	}
}

// ExecuteTemplate executes a template and writes its result to the w writer.
// The layout is the layout argument or, when that is empty, the one set by
// Layout. NoLayout renders the template without any.
//
// The engine loads itself first if it was never loaded.
// It returns a view.ErrTemplateNotFound for a template or a layout that
// does not exist. Nothing is written to w when the execution fails.
func (s *HTMLEngine) ExecuteTemplate(w io.Writer, name string, layout string, bindingData any) error {
	// parses the templates when they were never loaded, and
	// re-parses them if reload is enabled.
	if err := s.loader.ready(s.reload.Load(), s.build); err != nil {
		return err
	}

	set := s.set.Load()

	page, ok := set.resolve(name)
	if !ok {
		return ErrTemplateNotFound{Name: name, IsLayout: false, Data: bindingData}
	}

	target, hasLayout := page, false
	if layout = getLayout(layout, s.layout); layout != "" {
		if target, ok = set.resolve(layout); !ok {
			return ErrTemplateNotFound{Name: layout, IsLayout: true, Data: bindingData}
		}

		hasLayout = true
	}

	inst, err := set.get()
	if err != nil {
		return err
	}

	// The result is buffered, so that the instance goes back before the
	// first byte is sent: a slow client must not keep one busy.
	buf := getBuffer()
	defer putBuffer(buf)

	inst.page, inst.layout, inst.yielding, inst.depth = page, hasLayout, false, 0
	err = inst.root.ExecuteTemplate(buf, target, bindingData)
	set.put(inst)

	if err != nil {
		return err
	}

	_, err = w.Write(buf.Bytes())
	return err
}

var bufferPool = sync.Pool{New: func() any {
	return new(bytes.Buffer)
}}

func getBuffer() *bytes.Buffer {
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

// A buffer that grew for one very large page is left to the collector.
const maxPooledBuffer = 1 << 20

func putBuffer(buf *bytes.Buffer) {
	if buf.Cap() <= maxPooledBuffer {
		bufferPool.Put(buf)
	}
}
