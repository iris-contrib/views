package views_test

import (
	"bytes"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	texttemplate "text/template"

	"github.com/iris-contrib/views"

	"github.com/kataras/iris/v14"
	"github.com/kataras/iris/v14/httptest"
	"github.com/kataras/iris/v14/middleware/view"
)

// The fixtures of every engine share one shape, so that one table drives all
// six. Under testdata/<engine>:
//
//	index, about      a page that prints "page:<name> Hello <Name>"
//	partial           a page that pulls partials/footer in
//	extended          a page that uses the engine's own inheritance (if any)
//	layouts/main      "layout:main" around the page
//	layouts/other     "layout:other" around the page
//	partials/footer   "partial:footer <Name>"
//
// testdata/funcs/<engine>/funcs calls the template function "upper". It lives
// apart because html/template rejects an unknown function when it parses.
//
//go:embed testdata
var embedded embed.FS

// engine is what every engine of this package offers. A constructor result
// that does not satisfy all three view interfaces does not compile here.
type engine interface {
	view.Engine
	view.EngineFuncer
	view.EngineExtenser

	Load() error
	IsLoaded() bool
}

// config holds the settings the tests change. The setters of the six engines
// return their own types, so each table entry applies them itself.
type config struct {
	rootDir string // RootDir: the templates live in a sub directory of the fs.FS.
	layout  string // the engine's own default layout.
	reload  bool
}

type engineCase struct {
	name string // also the fixture directory under testdata.
	ext  string
	// layouts reports whether the engine honours the layout argument of
	// ExecuteTemplate. Django and Jet do not: a template names its own parent.
	layouts bool
	// extends reports whether the fixtures hold an "extended" page.
	extends bool
	// broken is a template source the engine must refuse to parse.
	broken string

	new   func(fsOrDir any, c config) engine
	parse func(e engine, name, contents string) error
}

func (tc engineCase) dir() string      { return "./testdata/" + tc.name }
func (tc engineCase) funcsDir() string { return "./testdata/funcs/" + tc.name }

var engines = []engineCase{
	{
		name: "html", ext: ".html", layouts: true, broken: "{{ .Name ",
		new: func(fsOrDir any, c config) engine {
			e := views.HTML(fsOrDir, ".html")
			if c.rootDir != "" {
				e.RootDir(c.rootDir)
			}
			return e.Layout(c.layout).Reload(c.reload)
		},
		parse: func(e engine, name, contents string) error {
			return e.(*views.HTMLEngine).ParseTemplate(name, []byte(contents), nil)
		},
	},
	{
		name: "django", ext: ".html", extends: true, broken: "{% if %}",
		new: func(fsOrDir any, c config) engine {
			e := views.Django(fsOrDir, ".html")
			if c.rootDir != "" {
				e.RootDir(c.rootDir)
			}
			return e.Reload(c.reload)
		},
		parse: func(e engine, name, contents string) error {
			return e.(*views.DjangoEngine).ParseTemplate(name, []byte(contents))
		},
	},
	{
		name: "handlebars", ext: ".html", layouts: true, broken: "{{#if Name}}never closed",
		new: func(fsOrDir any, c config) engine {
			e := views.Handlebars(fsOrDir, ".html")
			if c.rootDir != "" {
				e.RootDir(c.rootDir)
			}
			return e.Layout(c.layout).Reload(c.reload)
		},
		parse: func(e engine, name, contents string) error {
			return e.(*views.HandlebarsEngine).ParseTemplate(name, contents, nil)
		},
	},
	{
		name: "jet", ext: ".jet", extends: true, broken: "{{ if }}",
		new: func(fsOrDir any, c config) engine {
			e := views.Jet(fsOrDir, ".jet")
			if c.rootDir != "" {
				e.RootDir(c.rootDir)
			}
			return e.Reload(c.reload)
		},
		parse: func(e engine, name, contents string) error {
			return e.(*views.JetEngine).ParseTemplate(name, contents)
		},
	},
	{
		name: "pug", ext: ".pug", layouts: true, extends: true, broken: "p {{ .Name ",
		new: func(fsOrDir any, c config) engine {
			e := views.Pug(fsOrDir, ".pug")
			if c.rootDir != "" {
				e.RootDir(c.rootDir)
			}
			return e.Layout(c.layout).Reload(c.reload)
		},
		parse: func(e engine, name, contents string) error {
			return e.(*views.HTMLEngine).ParseTemplate(name, []byte(contents), nil)
		},
	},
	{
		name: "ace", ext: ".ace", layouts: true, broken: "p {{ .Name ",
		new: func(fsOrDir any, c config) engine {
			e := views.Ace(fsOrDir, ".ace")
			if c.rootDir != "" {
				e.RootDir(c.rootDir)
			}
			e.Layout(c.layout).Reload(c.reload)
			return e
		},
		parse: func(e engine, name, contents string) error {
			return e.(*views.AceEngine).ParseTemplate(name, []byte(contents), nil)
		},
	},
}

// forEachEngine runs fn as one subtest per engine.
func forEachEngine(t *testing.T, fn func(t *testing.T, tc engineCase)) {
	t.Helper()

	for _, tc := range engines {
		t.Run(tc.name, func(t *testing.T) {
			fn(t, tc)
		})
	}
}

var data = iris.Map{"Name": "iris"}

// render is the handler most tests use. A failure becomes a 500 that carries
// the error text, so an assertion shows the reason.
func render(name, layout string, data any) iris.Handler {
	return func(ctx iris.Context) {
		if err := view.Render(ctx, name, layout, data); err != nil {
			ctx.StopWithText(iris.StatusInternalServerError, err.Error())
		}
	}
}

// renderErr renders through the middleware and hands the error to the test.
func renderErr(dst *error, name, layout string, data any) iris.Handler {
	return func(ctx iris.Context) {
		*dst = view.Render(ctx, name, layout, data)
		if *dst != nil {
			ctx.StopWithStatus(iris.StatusInternalServerError)
		}
	}
}

// serve sends one request straight to the application's handler. Unlike the
// httpexpect client it may be called from many goroutines.
func serve(app *iris.Application, path string) (int, string) {
	w := httptest.NewRecorder()
	app.Handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w.Code, w.Body.String()
}

// copyDir copies a fixture directory, so a test can edit templates.
func copyDir(t *testing.T, src string) string {
	t.Helper()

	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	return dst
}

func writeFile(t *testing.T, name, contents string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// editFile replaces old with new inside a file.
func editFile(t *testing.T, name, old, new string) {
	t.Helper()

	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(old)) {
		t.Fatalf("%s does not contain %q", name, old)
	}
	writeFile(t, name, strings.ReplaceAll(string(b), old, new))
}

// The registration the framework documents in MIGRATION.md, HISTORY.md and its
// middleware reference: two lines and no Load call.
func TestDocumentedRegistration(t *testing.T) {
	app := iris.New()

	templates := views.HTML("./testdata/html", ".html")
	app.Use(view.Handler(templates))

	app.Get("/", func(ctx iris.Context) {
		if err := view.Render(ctx, "index.html", "layouts/main", data); err != nil {
			ctx.StopWithText(iris.StatusInternalServerError, err.Error())
		}
	})

	e := httptest.New(t, app)
	res := e.GET("/").Expect().Status(httptest.StatusOK)
	res.Header("Content-Type").Contains("text/html")
	res.Body().Contains("layout:main").Contains("page:index Hello iris")
}

// An engine that was never loaded loads itself on its first render.
func TestRenderWithoutLoad(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		templates := tc.new(tc.dir(), config{})
		if templates.IsLoaded() {
			t.Fatal("IsLoaded: true for an engine that has not rendered")
		}

		app := iris.New()
		app.Use(view.Handler(templates))
		app.Get("/", render("index"+tc.ext, "", data))
		app.Get("/again", render("about"+tc.ext, "", data))

		e := httptest.New(t, app)
		e.GET("/").Expect().Status(httptest.StatusOK).Body().Contains("page:index Hello iris")
		e.GET("/again").Expect().Status(httptest.StatusOK).Body().Contains("page:about Hello iris")

		if !templates.IsLoaded() {
			t.Fatal("IsLoaded: false after a render")
		}
	})
}

// An explicit Load is how a program fails fast, and it may be repeated.
func TestExplicitLoad(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		templates := tc.new(tc.dir(), config{})
		if err := templates.Load(); err != nil {
			t.Fatalf("Load: %v", err)
		}

		app := iris.New()
		app.Use(view.Handler(templates))
		app.Get("/", render("index"+tc.ext, "", data))

		e := httptest.New(t, app)
		e.GET("/").Expect().Status(httptest.StatusOK).Body().Contains("page:index Hello iris")

		// A second Load after a render parses everything again.
		if err := templates.Load(); err != nil {
			t.Fatalf("second Load: %v", err)
		}
		e.GET("/").Expect().Status(httptest.StatusOK).Body().Contains("page:index Hello iris")
	})
}

// A template that does not parse fails Load. Without Load the first render
// reports it, and so does every render after it: nothing panics.
func TestLoadError(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		dir := copyDir(t, tc.dir())
		writeFile(t, filepath.Join(dir, "broken"+tc.ext), tc.broken)

		if err := tc.new(dir, config{}).Load(); err == nil {
			t.Fatal("Load: expected an error for a template that does not parse")
		}

		var got error

		app := iris.New()
		app.Use(view.Handler(tc.new(dir, config{})))
		app.Get("/", renderErr(&got, "index"+tc.ext, "", data))

		e := httptest.New(t, app)
		for range 2 {
			got = nil
			e.GET("/").Expect().Status(httptest.StatusInternalServerError)
			if got == nil {
				t.Fatal("render: expected the load error")
			}
		}
	})
}

// A directory that does not exist, and a directory without one template of
// the engine's extension, are load errors as well.
func TestLoadNothing(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		// The error names the directory: a wrong one is the usual mistake.
		missing := filepath.Join(t.TempDir(), "missing")
		if err := tc.new(missing, config{}).Load(); err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("Load: expected an error that names the missing directory, got: %v", err)
		}

		empty := t.TempDir()
		err := tc.new(empty, config{}).Load()
		if !errors.Is(err, views.ErrNoTemplates) {
			t.Errorf("Load: expected ErrNoTemplates for an empty directory, got: %v", err)
		}

		var buf bytes.Buffer
		if err := tc.new(empty, config{}).ExecuteTemplate(&buf, "index"+tc.ext, "", nil); err == nil {
			t.Error("ExecuteTemplate: expected an error for an empty directory")
		}
	})
}

// A missing template and a missing layout are both a view.ErrTemplateNotFound.
func TestTemplateNotFound(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		var got error

		app := iris.New()
		app.Use(view.Handler(tc.new(tc.dir(), config{})))
		app.Get("/template", renderErr(&got, "missing"+tc.ext, "", data))
		app.Get("/layout", renderErr(&got, "index"+tc.ext, "layouts/missing"+tc.ext, data))

		e := httptest.New(t, app)

		e.GET("/template").Expect().Status(httptest.StatusInternalServerError)
		notFound, ok := errors.AsType[view.ErrTemplateNotFound](got)
		if !ok {
			t.Fatalf("missing template: expected a view.ErrTemplateNotFound, got: %T: %v", got, got)
		}
		if notFound.IsLayout || !strings.Contains(notFound.Name, "missing") {
			t.Errorf("missing template: unexpected error fields: %#v", notFound)
		}

		if !tc.layouts {
			return
		}

		e.GET("/layout").Expect().Status(httptest.StatusInternalServerError)
		notFound, ok = errors.AsType[view.ErrTemplateNotFound](got)
		if !ok {
			t.Fatalf("missing layout: expected a view.ErrTemplateNotFound, got: %T: %v", got, got)
		}
		if !notFound.IsLayout || !strings.Contains(notFound.Name, "layouts/missing") {
			t.Errorf("missing layout: unexpected error fields: %#v", notFound)
		}
	})
}

// A name resolves with or without the engine's extension and with or without
// a leading slash, the way Iris v12 resolved it before calling the engine.
func TestTemplateNames(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		app := iris.New()
		app.Use(view.Handler(tc.new(tc.dir(), config{})))
		app.Get("/bare", render("index", "", data))
		app.Get("/ext", render("index"+tc.ext, "", data))
		app.Get("/slash", render("/index"+tc.ext, "", data))
		if tc.layouts {
			app.Get("/layout-bare", render("index", "layouts/main", data))
			app.Get("/layout-slash", render("index", "/layouts/main"+tc.ext, data))
		}

		e := httptest.New(t, app)
		for _, path := range []string{"/bare", "/ext", "/slash"} {
			e.GET(path).Expect().Status(httptest.StatusOK).Body().Contains("page:index Hello iris")
		}
		if tc.layouts {
			for _, path := range []string{"/layout-bare", "/layout-slash"} {
				e.GET(path).Expect().Status(httptest.StatusOK).Body().
					Contains("layout:main").Contains("page:index Hello iris")
			}
		}
	})
}

// Layouts, from every place a layout can be set, and views.NoLayout switching
// each of them off.
func TestLayouts(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		if !tc.layouts {
			t.Skip("the engine ignores the layout argument, see TestLayoutIgnored")
		}

		main, other := "layouts/main"+tc.ext, "layouts/other"+tc.ext
		page := "index" + tc.ext

		app := iris.New()

		// No default layout on the engine.
		plain := app.Router("/plain", view.Handler(tc.new(tc.dir(), config{})))
		plain.Get("/none", render(page, "", data))
		plain.Get("/argument", render(page, main, data))
		plain.Get("/context", func(ctx iris.Context) {
			view.Layout(ctx, other)
			render(page, "", data)(ctx)
		})
		plain.Get("/argument-over-context", func(ctx iris.Context) {
			view.Layout(ctx, other)
			render(page, main, data)(ctx)
		})
		plain.Get("/no-layout-over-context", func(ctx iris.Context) {
			view.Layout(ctx, other)
			render(page, views.NoLayout, data)(ctx)
		})

		// view.WithLayout for a group of routes.
		group := app.Router("/group", view.Handler(tc.new(tc.dir(), config{})), view.WithLayout(other))
		group.Get("/middleware", render(page, "", data))
		group.Get("/argument", render(page, main, data))
		group.Get("/no-layout", render(page, views.NoLayout, data))

		// A default layout on the engine itself.
		engine := app.Router("/engine", view.Handler(tc.new(tc.dir(), config{layout: main})))
		engine.Get("/default", render(page, "", data))
		engine.Get("/argument", render(page, other, data))
		engine.Get("/context", func(ctx iris.Context) {
			view.Layout(ctx, other)
			render(page, "", data)(ctx)
		})
		engine.Get("/no-layout-argument", render(page, views.NoLayout, data))
		engine.Get("/no-layout-context", func(ctx iris.Context) {
			view.Layout(ctx, views.NoLayout)
			render(page, "", data)(ctx)
		})

		e := httptest.New(t, app)

		tests := []struct {
			path   string
			layout string // "" for none.
		}{
			{"/plain/none", ""},
			{"/plain/argument", "layout:main"},
			{"/plain/context", "layout:other"},
			{"/plain/argument-over-context", "layout:main"},
			{"/plain/no-layout-over-context", ""},
			{"/group/middleware", "layout:other"},
			{"/group/argument", "layout:main"},
			{"/group/no-layout", ""},
			{"/engine/default", "layout:main"},
			{"/engine/argument", "layout:other"},
			{"/engine/context", "layout:other"},
			{"/engine/no-layout-argument", ""},
			{"/engine/no-layout-context", ""},
		}

		for _, tt := range tests {
			body := e.GET(tt.path).Expect().Status(httptest.StatusOK).Body()
			body.Contains("page:index Hello iris")
			if tt.layout == "" {
				body.NotContains("layout:")
			} else {
				body.Contains(tt.layout)
			}
		}
	})
}

// Django and Jet templates name their own parent with "extends". The layout
// argument is ignored and must not break the render.
func TestLayoutIgnored(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		if tc.layouts {
			t.Skip("the engine honours the layout argument, see TestLayouts")
		}

		app := iris.New()
		app.Use(view.Handler(tc.new(tc.dir(), config{})))
		app.Get("/argument", render("index"+tc.ext, "layouts/main"+tc.ext, data))
		app.Get("/context", view.WithLayout("layouts/main"+tc.ext), render("index"+tc.ext, "", data))
		app.Get("/no-layout", render("index"+tc.ext, views.NoLayout, data))

		e := httptest.New(t, app)
		for _, path := range []string{"/argument", "/context", "/no-layout"} {
			e.GET(path).Expect().Status(httptest.StatusOK).Body().
				Contains("page:index Hello iris").NotContains("layout:")
		}
	})
}

// The engine's own inheritance: "extends" in Django, Jet and Pug.
func TestExtends(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		if !tc.extends {
			t.Skip("no template inheritance in this engine")
		}

		app := iris.New()
		app.Use(view.Handler(tc.new(tc.dir(), config{})))
		app.Get("/", render("extended"+tc.ext, "", data))

		httptest.New(t, app).GET("/").Expect().Status(httptest.StatusOK).Body().
			Contains("layout:").Contains("page:extended Hello iris")
	})
}

// A page that pulls a partial in, with the page's data.
func TestPartials(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		app := iris.New()
		app.Use(view.Handler(tc.new(tc.dir(), config{})))
		app.Get("/", render("partial"+tc.ext, "", data))

		httptest.New(t, app).GET("/").Expect().Status(httptest.StatusOK).Body().
			Contains("page:partial").Contains("partial:footer iris")
	})
}

// Data set on the request by view.WithData, view.Data and view.KeyValue
// reaches the template when the handler passes none.
func TestContextData(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		page := "index" + tc.ext

		app := iris.New()
		app.Use(view.Handler(tc.new(tc.dir(), config{})))

		app.Get("/with-data", view.WithData(iris.Map{"Name": "middleware"}), render(page, "", nil))
		app.Get("/key-value", func(ctx iris.Context) {
			if err := view.KeyValue(ctx, "Name", "key-value"); err != nil {
				ctx.StopWithText(iris.StatusInternalServerError, err.Error())
				return
			}
			render(page, "", nil)(ctx)
		})
		shared := iris.Map{"Name": "middleware"}
		app.Get("/both", view.WithData(shared), func(ctx iris.Context) {
			if ctx.Query().GetString("override") != "" {
				view.KeyValue(ctx, "Name", "overridden")
			}
			render(page, "", nil)(ctx)
		})
		app.Get("/argument", view.WithData(iris.Map{"Name": "middleware"}), render(page, "", data))
		if tc.layouts {
			app.Get("/layout", view.WithData(iris.Map{"Name": "middleware"}), render(page, "layouts/main", nil))
		}

		e := httptest.New(t, app)
		e.GET("/with-data").Expect().Status(httptest.StatusOK).Body().Contains("Hello middleware")
		e.GET("/key-value").Expect().Status(httptest.StatusOK).Body().Contains("Hello key-value")
		e.GET("/argument").Expect().Status(httptest.StatusOK).Body().Contains("Hello iris")
		if tc.layouts {
			e.GET("/layout").Expect().Status(httptest.StatusOK).Body().
				Contains("layout:main").Contains("Hello middleware")
		}
		// view.WithData hands every request the same map. view.KeyValue
		// writes to a copy of it that belongs to the request, and neither
		// the middleware nor an engine writes to the map itself: the next
		// request reads what the application put there.
		e.GET("/both").WithQuery("override", "1").Expect().Status(httptest.StatusOK).Body().Contains("Hello overridden")
		e.GET("/both").Expect().Status(httptest.StatusOK).Body().Contains("Hello middleware")
		if len(shared) != 1 || shared["Name"] != "middleware" {
			t.Fatalf("the map given to view.WithData was changed: %v", shared)
		}
	})
}

// A function that depends on the request is not for AddFunc, which registers
// on the engine. It travels in the data of the render, and a template of the
// html/template based engines calls it with the "call" action.
func TestFuncInData(t *testing.T) {
	sources := map[string]struct {
		ext      string
		contents string
		new      func() engine
	}{
		"html": {".html", `{{ call .Greet .Name }}`, func() engine { return views.HTML(nil, ".html") }},
		"pug":  {".pug", `p {{ call .Greet .Name }}`, func() engine { return views.Pug(nil, ".pug") }},
		"ace":  {".ace", `p {{ call .Greet .Name }}`, func() engine { return views.Ace(nil, ".ace") }},
	}

	for name, src := range sources {
		t.Run(name, func(t *testing.T) {
			templates := src.new()
			if err := templates.(interface {
				ParseTemplate(string, []byte, template.FuncMap) error
			}).ParseTemplate("page"+src.ext, []byte(src.contents), nil); err != nil {
				t.Fatal(err)
			}

			app := iris.New()
			app.Use(view.Handler(templates))
			app.Get("/", func(ctx iris.Context) {
				view.KeyValue(ctx, "Name", "iris")
				view.KeyValue(ctx, "Greet", func(name string) string {
					return ctx.Query().GetString("lang") + ": " + name
				})
				render("page"+src.ext, "", nil)(ctx)
			})

			httptest.New(t, app).GET("/").WithQuery("lang", "en").Expect().
				Status(httptest.StatusOK).Body().Contains("en: iris")
		})
	}
}

// A struct works as template data too.
func TestStructData(t *testing.T) {
	type page struct{ Name string }

	forEachEngine(t, func(t *testing.T, tc engineCase) {
		app := iris.New()
		app.Use(view.Handler(tc.new(tc.dir(), config{})))
		app.Get("/value", render("index"+tc.ext, "", page{Name: "struct"}))
		app.Get("/pointer", render("index"+tc.ext, "", &page{Name: "pointer"}))

		e := httptest.New(t, app)
		e.GET("/value").Expect().Status(httptest.StatusOK).Body().Contains("Hello struct")
		e.GET("/pointer").Expect().Status(httptest.StatusOK).Body().Contains("Hello pointer")
	})
}

// Template functions, from the engine and from the middleware.
func TestFuncs(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		page := "funcs" + tc.ext
		const want = "page:funcs IRIS"

		t.Run("AddFunc before Load", func(t *testing.T) {
			templates := tc.new(tc.funcsDir(), config{})
			templates.AddFunc("upper", strings.ToUpper)
			if err := templates.Load(); err != nil {
				t.Fatal(err)
			}

			app := iris.New()
			app.Use(view.Handler(templates))
			app.Get("/", render(page, "", nil))

			httptest.New(t, app).GET("/").Expect().Status(httptest.StatusOK).Body().Contains(want)
		})

		t.Run("view.WithFuncMap", func(t *testing.T) {
			app := iris.New()
			app.Use(view.Handler(tc.new(tc.funcsDir(), config{})))
			app.Use(view.WithFuncMap(texttemplate.FuncMap{"upper": strings.ToUpper}))
			app.Get("/", render(page, "", nil))

			e := httptest.New(t, app)
			// The middleware adds the function on every request.
			for range 3 {
				e.GET("/").Expect().Status(httptest.StatusOK).Body().Contains(want)
			}
		})

		t.Run("view.AddFunc", func(t *testing.T) {
			app := iris.New()
			app.Use(view.Handler(tc.new(tc.funcsDir(), config{})))
			app.Get("/", func(ctx iris.Context) {
				if !view.AddFunc(ctx, "upper", strings.ToUpper) {
					ctx.StopWithText(iris.StatusInternalServerError, "the engine is not a view.EngineFuncer")
					return
				}
				render(page, "", nil)(ctx)
			})

			e := httptest.New(t, app)
			for range 3 {
				e.GET("/").Expect().Status(httptest.StatusOK).Body().Contains(want)
			}
		})

		t.Run("reload", func(t *testing.T) {
			templates := tc.new(tc.funcsDir(), config{reload: true})
			templates.AddFunc("upper", strings.ToUpper)

			app := iris.New()
			app.Use(view.Handler(templates))
			app.Get("/", render(page, "", nil))

			e := httptest.New(t, app)
			for range 2 {
				e.GET("/").Expect().Status(httptest.StatusOK).Body().Contains(want)
			}
		})
	})
}

// A value that is not a usable function is reported as an error, not a panic.
func TestInvalidFunc(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		templates := tc.new(tc.dir(), config{})
		templates.AddFunc("broken", 42)

		var buf bytes.Buffer
		// Whether the engine refuses the value or keeps it as a plain
		// variable is its own business. It must not panic.
		_ = templates.ExecuteTemplate(&buf, "index"+tc.ext, "", data)
	})
}

func TestExt(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		app := iris.New()
		app.Use(view.Handler(tc.new(tc.dir(), config{})))
		app.Get("/", func(ctx iris.Context) {
			ctx.WriteString(view.Ext(ctx))
		})

		httptest.New(t, app).GET("/").Expect().Status(httptest.StatusOK).Body().IsEqual(tc.ext)
	})
}

func TestWithTemplate(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		tmpl := &view.Template{Name: "index" + tc.ext, Data: data}
		if tc.layouts {
			tmpl.Layout = "layouts/main" + tc.ext
		}

		app := iris.New()
		app.Use(view.Handler(tc.new(tc.dir(), config{})))
		app.Get("/", view.WithTemplate(tmpl))
		app.Get("/teapot", view.WithTemplate(&view.Template{
			Code: iris.StatusTeapot,
			Name: "about" + tc.ext,
			DataFunc: func(ctx iris.Context) (any, error) {
				return iris.Map{"Name": ctx.Query().GetString("name")}, nil
			},
		}))
		app.Get("/missing", view.WithTemplate(&view.Template{
			Code: iris.StatusInternalServerError,
			Name: "missing" + tc.ext,
		}))

		e := httptest.New(t, app)

		body := e.GET("/").Expect().Status(httptest.StatusOK).Body()
		body.Contains("page:index Hello iris")
		if tc.layouts {
			body.Contains("layout:main")
		}

		e.GET("/teapot").WithQuery("name", "query").Expect().Status(iris.StatusTeapot).
			Body().Contains("page:about Hello query")

		// The template error is private: the client must not read it.
		e.GET("/missing").Expect().Status(httptest.StatusInternalServerError).
			Body().NotContains("missing")
	})
}

// Templates embedded with embed.FS, found through RootDir or through fs.Sub.
func TestEmbedded(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		sub, err := fs.Sub(embedded, "testdata/"+tc.name)
		if err != nil {
			t.Fatal(err)
		}

		testSources(t, tc, map[string]engine{
			"RootDir": tc.new(embedded, config{rootDir: "testdata/" + tc.name}),
			"fs.Sub":  tc.new(sub, config{}),
		})
	})
}

// Templates from an http.FileSystem. The ones in sub directories are the
// point: the framework did not find those before v14.0.0.
func TestHTTPFileSystem(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		testSources(t, tc, map[string]engine{
			"http.Dir": tc.new(http.Dir(tc.dir()), config{}),
			"RootDir":  tc.new(http.Dir("./testdata"), config{rootDir: tc.name}),
		})
	})
}

// testSources renders, from each of the given engines, a page, a page that
// pulls in a partial of a sub directory, and a page inside a layout or a
// parent of a sub directory.
func testSources(t *testing.T, tc engineCase, sources map[string]engine) {
	t.Helper()

	for name, templates := range sources {
		t.Run(name, func(t *testing.T) {
			app := iris.New()
			app.Use(view.Handler(templates))
			app.Get("/", render("index"+tc.ext, "", data))
			app.Get("/partial", render("partial"+tc.ext, "", data))
			if tc.layouts {
				app.Get("/layout", render("index"+tc.ext, "layouts/main"+tc.ext, data))
			}
			if tc.extends {
				app.Get("/extended", render("extended"+tc.ext, "", data))
			}

			e := httptest.New(t, app)
			e.GET("/").Expect().Status(httptest.StatusOK).Body().Contains("page:index Hello iris")
			e.GET("/partial").Expect().Status(httptest.StatusOK).Body().Contains("partial:footer iris")
			if tc.layouts {
				e.GET("/layout").Expect().Status(httptest.StatusOK).Body().
					Contains("layout:main").Contains("page:index Hello iris")
			}
			if tc.extends {
				e.GET("/extended").Expect().Status(httptest.StatusOK).Body().
					Contains("layout:").Contains("page:extended Hello iris")
			}
		})
	}
}

// Two engines of the same kind in one process, one per router, each with its
// own templates and its own function under the same name.
func TestTwoEngines(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		firstDir, secondDir := copyDir(t, tc.dir()), copyDir(t, tc.dir())
		editFile(t, filepath.Join(secondDir, "index"+tc.ext), "page:index", "page:second")

		for _, dir := range []string{firstDir, secondDir} {
			b, err := os.ReadFile(filepath.Join(tc.funcsDir(), "funcs"+tc.ext))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(dir, "funcs"+tc.ext), string(b))
		}

		first := tc.new(firstDir, config{})
		first.AddFunc("upper", strings.ToUpper)
		second := tc.new(secondDir, config{})
		second.AddFunc("upper", func(s string) string { return "second-" + s })

		// One is loaded up front and the other on its first render.
		if err := first.Load(); err != nil {
			t.Fatal(err)
		}

		app := iris.New()
		a := app.Router("/first", view.Handler(first))
		a.Get("/", render("index"+tc.ext, "", data))
		a.Get("/funcs", render("funcs"+tc.ext, "", data))
		b := app.Router("/second", view.Handler(second))
		b.Get("/", render("index"+tc.ext, "", data))
		b.Get("/funcs", render("funcs"+tc.ext, "", data))

		e := httptest.New(t, app)
		for range 2 {
			e.GET("/first").Expect().Status(httptest.StatusOK).Body().Contains("page:index Hello iris")
			e.GET("/second").Expect().Status(httptest.StatusOK).Body().Contains("page:second Hello iris")
			e.GET("/first/funcs").Expect().Status(httptest.StatusOK).Body().Contains("page:funcs IRIS")
			e.GET("/second/funcs").Expect().Status(httptest.StatusOK).Body().Contains("page:funcs second-iris")
		}
	})
}

// countingFS counts how many times the root directory is listed, which is
// once per walk and so once per load.
type countingFS struct {
	fs.FS
	walks atomic.Int64
}

func (c *countingFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name == "." {
		c.walks.Add(1)
	}
	return fs.ReadDir(c.FS, name)
}

// Many first requests at once load the engine one time, and every one of
// them renders. Run with -race.
func TestConcurrentFirstRequests(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		fsys := &countingFS{FS: os.DirFS(tc.dir())}

		app := iris.New()
		app.Use(view.Handler(tc.new(fsys, config{})))
		app.Get("/index", render("index"+tc.ext, "", data))
		app.Get("/about", render("about"+tc.ext, "", data))
		httptest.New(t, app) // builds the application.

		const requests = 64

		var (
			wg    sync.WaitGroup
			start = make(chan struct{})
			fails atomic.Int64
		)

		for i := range requests {
			page := "index"
			if i%2 == 1 {
				page = "about"
			}

			wg.Go(func() {
				<-start
				code, body := serve(app, "/"+page)
				if code != http.StatusOK || !strings.Contains(body, "page:"+page+" Hello iris") {
					fails.Add(1)
					t.Errorf("%s: %d: %s", page, code, body)
				}
			})
		}

		close(start)
		wg.Wait()

		if n := fsys.walks.Load(); n != 1 {
			t.Errorf("expected one load for %d concurrent first requests, got %d", requests, n)
		}
	})
}

// Two pages that share a layout, rendered at the same time: each response
// must hold its own page. Run with -race.
func TestConcurrentLayoutRenders(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		if !tc.layouts {
			t.Skip("the engine ignores the layout argument")
		}

		app := iris.New()
		app.Use(view.Handler(tc.new(tc.dir(), config{})))
		app.Get("/index", render("index"+tc.ext, "layouts/main"+tc.ext, data))
		app.Get("/about", render("about"+tc.ext, "layouts/main"+tc.ext, data))
		httptest.New(t, app)

		const (
			workers = 8
			rounds  = 250
		)

		var wg sync.WaitGroup
		for i := range workers {
			page, other := "index", "about"
			if i%2 == 1 {
				page, other = other, page
			}

			wg.Go(func() {
				for range rounds {
					code, body := serve(app, "/"+page)
					if code != http.StatusOK || !strings.Contains(body, "layout:main") ||
						!strings.Contains(body, "page:"+page) || strings.Contains(body, "page:"+other) {
						t.Errorf("%s: %d: %s", page, code, body)
						return
					}
				}
			})
		}
		wg.Wait()
	})
}

// Functions added from requests while other requests render. Run with -race.
func TestConcurrentAddFunc(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		app := iris.New()
		app.Use(view.Handler(tc.new(tc.funcsDir(), config{})))
		app.Use(view.WithFuncMap(texttemplate.FuncMap{"upper": strings.ToUpper}))
		app.Get("/", render("funcs"+tc.ext, "", data))
		httptest.New(t, app)

		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				for range 50 {
					if code, body := serve(app, "/"); code != http.StatusOK || !strings.Contains(body, "page:funcs IRIS") {
						t.Errorf("%d: %s", code, body)
						return
					}
				}
			})
		}
		wg.Wait()
	})
}

// Reload(true) parses the templates again on every render. Without it an
// edit is not seen until the next Load.
func TestReload(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		index := "index" + tc.ext

		t.Run("on", func(t *testing.T) {
			dir := copyDir(t, tc.dir())

			app := iris.New()
			app.Use(view.Handler(tc.new(dir, config{reload: true})))
			app.Get("/", render(index, "", data))

			e := httptest.New(t, app)
			e.GET("/").Expect().Status(httptest.StatusOK).Body().Contains("page:index")

			editFile(t, filepath.Join(dir, index), "page:index", "page:edited")
			e.GET("/").Expect().Status(httptest.StatusOK).Body().Contains("page:edited")

			// A template that stops parsing is an error, and fixing it is
			// enough to render again.
			b, err := os.ReadFile(filepath.Join(dir, index))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(dir, index), tc.broken)
			e.GET("/").Expect().Status(httptest.StatusInternalServerError)

			writeFile(t, filepath.Join(dir, index), string(b))
			e.GET("/").Expect().Status(httptest.StatusOK).Body().Contains("page:edited")
		})

		t.Run("off", func(t *testing.T) {
			dir := copyDir(t, tc.dir())
			templates := tc.new(dir, config{})

			app := iris.New()
			app.Use(view.Handler(templates))
			app.Get("/", render(index, "", data))

			e := httptest.New(t, app)
			e.GET("/").Expect().Status(httptest.StatusOK).Body().Contains("page:index")

			editFile(t, filepath.Join(dir, index), "page:index", "page:edited")
			e.GET("/").Expect().Status(httptest.StatusOK).Body().Contains("page:index")

			if err := templates.Load(); err != nil {
				t.Fatal(err)
			}
			e.GET("/").Expect().Status(httptest.StatusOK).Body().Contains("page:edited")
		})
	})
}

// Templates given as text with ParseTemplate, with and without a directory.
func TestParseTemplate(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		b, err := os.ReadFile(filepath.Join(tc.dir(), "index"+tc.ext))
		if err != nil {
			t.Fatal(err)
		}
		custom := strings.ReplaceAll(string(b), "page:index", "page:custom")
		later := strings.ReplaceAll(string(b), "page:index", "page:later")

		t.Run("alone", func(t *testing.T) {
			templates := tc.new(nil, config{})
			if err := tc.parse(templates, "custom"+tc.ext, custom); err != nil {
				t.Fatal(err)
			}

			app := iris.New()
			app.Use(view.Handler(templates))
			app.Get("/", render("custom"+tc.ext, "", data))

			httptest.New(t, app).GET("/").Expect().Status(httptest.StatusOK).Body().Contains("page:custom Hello iris")
		})

		t.Run("next to a directory", func(t *testing.T) {
			templates := tc.new(tc.dir(), config{})
			if err := tc.parse(templates, "custom"+tc.ext, custom); err != nil {
				t.Fatal(err)
			}

			app := iris.New()
			app.Use(view.Handler(templates))
			app.Get("/custom", render("custom"+tc.ext, "", data))
			app.Get("/index", render("index"+tc.ext, "", data))
			app.Get("/later", render("later"+tc.ext, "", data))

			e := httptest.New(t, app)
			e.GET("/custom").Expect().Status(httptest.StatusOK).Body().Contains("page:custom Hello iris")
			e.GET("/index").Expect().Status(httptest.StatusOK).Body().Contains("page:index Hello iris")

			// Added after the engine has rendered.
			if err := tc.parse(templates, "later"+tc.ext, later); err != nil {
				t.Fatal(err)
			}
			e.GET("/later").Expect().Status(httptest.StatusOK).Body().Contains("page:later Hello iris")
			e.GET("/custom").Expect().Status(httptest.StatusOK).Body().Contains("page:custom Hello iris")
		})

		t.Run("syntax error", func(t *testing.T) {
			if err := tc.parse(tc.new(nil, config{}), "broken"+tc.ext, tc.broken); err == nil {
				t.Fatal("expected an error for a template that does not parse")
			}
		})
	})
}

// An engine is an io.Writer renderer on its own, with no application around
// it: that is how a program renders an e-mail body.
func TestExecuteTemplateDirect(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		templates := tc.new(tc.dir(), config{})

		var buf bytes.Buffer
		if err := templates.ExecuteTemplate(&buf, "index"+tc.ext, "", map[string]any{"Name": "writer"}); err != nil {
			t.Fatal(err)
		}
		if got := buf.String(); !strings.Contains(got, "page:index Hello writer") {
			t.Fatalf("unexpected output: %s", got)
		}

		err := templates.ExecuteTemplate(&buf, "missing"+tc.ext, "", nil)
		if _, ok := errors.AsType[view.ErrTemplateNotFound](err); !ok {
			t.Fatalf("expected a view.ErrTemplateNotFound, got: %T: %v", err, err)
		}
	})
}

// Data the engine cannot read is an error, not a panic.
func TestUnsupportedData(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		templates := tc.new(tc.dir(), config{})

		var buf bytes.Buffer
		for _, v := range []any{"text", 42, []string{"a"}, (*struct{ Name string })(nil)} {
			// The result depends on the engine: html/template prints "<no
			// value>" where Django refuses anything but a map or a struct.
			_ = templates.ExecuteTemplate(&buf, "index"+tc.ext, "", v)
			if tc.layouts {
				_ = templates.ExecuteTemplate(&buf, "index"+tc.ext, "layouts/main"+tc.ext, v)
			}
		}
	})
}

// A layout renders with no data at all.
func TestLayoutWithoutData(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		if !tc.layouts {
			t.Skip("the engine ignores the layout argument")
		}

		app := iris.New()
		app.Use(view.Handler(tc.new(tc.dir(), config{})))
		app.Get("/", render("index"+tc.ext, "layouts/main"+tc.ext, nil))

		httptest.New(t, app).GET("/").Expect().Status(httptest.StatusOK).Body().
			Contains("layout:main").Contains("page:index Hello")
	})
}

// The layout functions of the html/template based engines.
func TestHTMLLayoutFuncs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "users/index.html"), `page:users`)
	writeFile(t, filepath.Join(dir, "users/index.script.html"), `script:users`)
	writeFile(t, filepath.Join(dir, "orders/index.html"), `page:orders`)
	writeFile(t, filepath.Join(dir, "layouts/main.html"),
		`[{{ current }}][{{ yield . }}][{{ partial_r ".script" . }}][{{ render "users/index.script.html" . }}]`)

	templates := views.HTML(dir, ".html")

	for page, want := range map[string]string{
		"users/index.html":  "[users/index.html][page:users][script:users][script:users]",
		"orders/index.html": "[orders/index.html][page:orders][][script:users]",
	} {
		var buf bytes.Buffer
		if err := templates.ExecuteTemplate(&buf, page, "layouts/main.html", nil); err != nil {
			t.Fatal(err)
		}
		if got := buf.String(); got != want {
			t.Errorf("%s:\nexpected: %s\n     got: %s", page, want, got)
		}
	}
}

// A function given to one template with ParseTemplate, and options that reach
// the parser.
func TestHTMLOptions(t *testing.T) {
	templates := views.HTML(nil, ".html").Delims("<<", ">>").Option("missingkey=error")
	err := templates.ParseTemplate("hello.html", []byte(`<< greet .Name >>`), template.FuncMap{
		"greet": func(s string) string { return "Greetings " + s + "!" },
	})
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err = templates.ExecuteTemplate(&buf, "hello.html", "", map[string]any{"Name": "iris"}); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "Greetings iris!" {
		t.Fatalf("unexpected output: %s", got)
	}

	if err = templates.ExecuteTemplate(&buf, "hello.html", "", map[string]any{}); err == nil {
		t.Fatal("expected an error for a missing key")
	}
}

// A partial that does not exist fails the render with the same error type a
// missing page does.
func TestPartialNotFound(t *testing.T) {
	sources := map[string]struct {
		ext      string
		contents string
		new      func(dir string) engine
	}{
		"html":       {".html", `{{ render "partials/missing.html" . }}`, func(dir string) engine { return views.HTML(dir, ".html") }},
		"handlebars": {".html", `{{ render "partials/missing.html" . }}`, func(dir string) engine { return views.Handlebars(dir, ".html") }},
	}

	for name, src := range sources {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "page"+src.ext), src.contents)

			var buf bytes.Buffer
			err := src.new(dir).ExecuteTemplate(&buf, "page"+src.ext, "", nil)
			if err == nil || !strings.Contains(err.Error(), "partials/missing.html") {
				t.Fatalf("expected an error that names the partial, got: %v", err)
			}
			if buf.Len() > 0 {
				t.Fatalf("nothing should be written when a render fails, got: %s", buf.String())
			}
		})
	}
}

// A template that renders itself is an error, not a stack overflow.
func TestHTMLRecursion(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "loop.html"), `{{ render "loop.html" . }}`)
	writeFile(t, filepath.Join(dir, "layouts/loop.html"), `[{{ yield . }}]`)

	templates := views.HTML(dir, ".html")

	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, "loop.html", "", nil); err == nil {
		t.Fatal("expected an error for a template that renders itself")
	}

	// A layout rendered as its own page: yield prints nothing the second time.
	if err := templates.ExecuteTemplate(&buf, "layouts/loop.html", "layouts/loop.html", nil); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "[[]]" {
		t.Fatalf("unexpected output: %s", got)
	}
}

// The include helper of Ace and the include keyword of Pug, both with the
// file extension.
func TestIncludes(t *testing.T) {
	t.Run("ace", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "page.ace"), "div\n  = include partials/footer.ace .\n")
		writeFile(t, filepath.Join(dir, "partials/footer.ace"), "footer partial:footer {{.Name}}\n")

		var buf bytes.Buffer
		if err := views.Ace(dir, ".ace").ExecuteTemplate(&buf, "page", "", data); err != nil {
			t.Fatal(err)
		}
		if got := buf.String(); got != "<div><footer>partial:footer iris</footer></div>" {
			t.Fatalf("unexpected output: %s", got)
		}
	})

	t.Run("pug", func(t *testing.T) {
		var buf bytes.Buffer
		if err := views.Pug("./testdata/pug", ".pug").ExecuteTemplate(&buf, "partial", "", data); err != nil {
			t.Fatal(err)
		}
		if got := buf.String(); !strings.Contains(got, "<footer>partial:footer iris</footer>") {
			t.Fatalf("unexpected output: %s", got)
		}
	})
}

// Ace indents its output on request and minifies it otherwise.
func TestAceIndent(t *testing.T) {
	var buf bytes.Buffer
	if err := views.Ace("./testdata/ace", ".ace").SetIndent("  ").ExecuteTemplate(&buf, "partial", "", data); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "<div>\n  <p>") {
		t.Fatalf("expected indented output, got: %q", got)
	}
}

// Filters and globals of the Django engine. Filters are process-wide in
// pongo2, so a second registration replaces the first instead of failing.
func TestDjangoFiltersAndGlobals(t *testing.T) {
	shout := func(in *views.Value, _ *views.Value) (*views.Value, *views.Error) {
		return views.AsValue(strings.ToUpper(in.String()) + "!"), nil
	}

	first := views.Django(nil, ".html").AddFilter("views_test_shout", shout)
	second := views.Django(nil, ".html").RegisterFilter("views_test_shout", shout)

	for _, templates := range []*views.DjangoEngine{first, second} {
		templates.AddFunc("site", "example.com")
		templates.AddFunc("double", func(n int) int { return n * 2 })

		if err := templates.ParseTemplate("page.html", []byte(`{{ Name|views_test_shout }} {{ site }} {{ double(21) }}`)); err != nil {
			t.Fatal(err)
		}

		var buf bytes.Buffer
		if err := templates.ExecuteTemplate(&buf, "page.html", "", data); err != nil {
			t.Fatal(err)
		}
		if got := buf.String(); got != "IRIS! example.com 42" {
			t.Fatalf("unexpected output: %s", got)
		}
	}
}

// Helpers of the Handlebars engine: for one engine, for one template, and
// for every engine of the process.
func TestHandlebarsHelpers(t *testing.T) {
	templates := views.Handlebars(nil, ".html")
	templates.AddFunc("bold", func(s string) string { return "<b>" + s + "</b>" })
	// Twice: raymond panics for a name it has, the engine replaces it.
	templates.AddGlobalFunc("views_test_site", func() string { return "first" })
	templates.AddGlobalFunc("views_test_site", func() string { return "example.com" })

	err := templates.ParseTemplate("page.html", `{{{bold Name}}} {{views_test_site}} {{local}}`, template.FuncMap{
		"local": func() string { return "local" },
	})
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err = templates.ExecuteTemplate(&buf, "page.html", "", data); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "<b>iris</b> example.com local" {
		t.Fatalf("unexpected output: %s", got)
	}
}

// Global variables of the Jet engine, and runtime variables set on a request.
func TestJetVars(t *testing.T) {
	templates := views.Jet(nil, ".jet")
	templates.AddVar("site", "example.com")
	if err := templates.ParseTemplate("page.jet", `{{ site }} {{ user }} {{ .Name }}`); err != nil {
		t.Fatal(err)
	}

	app := iris.New()
	app.Use(view.Handler(templates))
	app.Get("/", func(ctx iris.Context) {
		views.AddJetRuntimeVars(ctx, views.JetRuntimeVars{"user": reflect.ValueOf("makis")})
		render("page.jet", "", data)(ctx)
	})

	httptest.New(t, app).GET("/").Expect().Status(httptest.StatusOK).Body().IsEqual("example.com makis iris")
}

// The exported Templates field is a copy of the loaded set. Reading it, and
// even executing it, leaves the renders of the engine alone.
func TestHTMLTemplatesField(t *testing.T) {
	templates := views.HTML("./testdata/html", ".html")
	if templates.Templates != nil {
		t.Fatal("expected a nil Templates before the first load")
	}

	if err := templates.Load(); err != nil {
		t.Fatal(err)
	}

	if templates.Templates.Lookup("layouts/main.html") == nil {
		t.Fatal("expected the loaded templates in the Templates field")
	}

	var buf bytes.Buffer
	if err := templates.Templates.ExecuteTemplate(&buf, "index.html", data); err != nil {
		t.Fatal(err)
	}

	buf.Reset()
	if err := templates.ExecuteTemplate(&buf, "index.html", "layouts/main.html", data); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "layout:main") || !strings.Contains(got, "page:index Hello iris") {
		t.Fatalf("unexpected output: %s", got)
	}
}
