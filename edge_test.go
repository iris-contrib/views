package views_test

import (
	"bytes"
	"errors"
	"html/template"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/iris-contrib/views"

	"github.com/flosch/pongo2/v4"
	"github.com/kataras/iris/v14"
	"github.com/kataras/iris/v14/httptest"
	"github.com/kataras/iris/v14/middleware/view"
)

// A template that pulls itself in is an error. The parsers behind these
// engines recurse until the stack or the memory runs out, and Go cannot
// recover from either, so the engines have to cut the recursion themselves.
//
// Jet runs "include" when a template executes, inside jet. That one case is
// out of the reach of the engine and is not here.
func TestSelfReference(t *testing.T) {
	tests := []struct {
		name   string
		engine string
		files  map[string]string
	}{
		{"html template", "html", map[string]string{"a.html": `{{ template "a.html" . }}`}},
		{"html render", "html", map[string]string{"a.html": `{{ render "a.html" . }}`}},
		{"ace include", "ace", map[string]string{"a.ace": "= include a.ace .\n"}},
		{"handlebars render", "handlebars", map[string]string{"a.html": `{{ render "a.html" . }}`}},
		{"handlebars render in turns", "handlebars", map[string]string{
			"a.html": `{{ render "b.html" . }}`,
			"b.html": `{{ render "a.html" . }}`,
		}},
		{"django include", "django", map[string]string{"a.html": `{% include "a.html" %}`}},
		{"django extends", "django", map[string]string{"a.html": `{% extends "a.html" %}`}},
		{"django extends in turns", "django", map[string]string{
			"a.html": `{% extends "b.html" %}`,
			"b.html": `{% extends "a.html" %}`,
		}},
		{"pug include", "pug", map[string]string{"a.pug": "include a.pug\n"}},
		{"pug extends", "pug", map[string]string{"a.pug": "extends a.pug\n"}},
		{"jet extends", "jet", map[string]string{"a.jet": `{{ extends "a.jet" }}`}},
		{"jet extends in turns", "jet", map[string]string{
			"a.jet": `{{ extends "b.jet" }}`,
			"b.jet": `{{ extends "a.jet" }}`,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tc engineCase
			for _, e := range engines {
				if e.name == tt.engine {
					tc = e
				}
			}

			dir := t.TempDir()
			for name, contents := range tt.files {
				writeFile(t, filepath.Join(dir, name), contents)
			}

			var buf bytes.Buffer
			if err := tc.new(dir, config{}).ExecuteTemplate(&buf, "a"+tc.ext, "", data); err == nil {
				t.Fatalf("expected an error, got the output: %s", buf.String())
			}
		})
	}
}

// The guard of the Handlebars render helper counts depth, not calls: a
// template that renders itself for every child of a tree is fine.
func TestHandlebarsRecursionWithAnEnd(t *testing.T) {
	templates := views.Handlebars(nil, ".html")
	err := templates.ParseTemplate("node.html", `<{{name}}>{{#each children}}{{ render "node.html" this }}{{/each}}`, nil)
	if err != nil {
		t.Fatal(err)
	}

	tree := map[string]any{
		"name": "a",
		"children": []map[string]any{
			{"name": "b", "children": []map[string]any{{"name": "c"}, {"name": "d"}}},
			{"name": "e"},
		},
	}

	var buf bytes.Buffer
	if err = templates.ExecuteTemplate(&buf, "node.html", "", tree); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "<a><b><c><d><e>" {
		t.Fatalf("unexpected output: %s", got)
	}
}

// A template function that panics, or that is called with an argument it
// does not take, is the error of the render in every engine.
func TestFuncPanics(t *testing.T) {
	funcs := map[string]any{
		"a panic with a string": func(s string) string { panic("boom") },
		"a panic with an error": func(s string) string { panic(errors.New("boom")) },
		"a runtime error": func(s string) string {
			return s[len(s)+1:] // out of range.
		},
		"a wrong argument type": func(n int) string { return "" },
	}

	forEachEngine(t, func(t *testing.T, tc engineCase) {
		for name, fn := range funcs {
			t.Run(name, func(t *testing.T) {
				templates := tc.new(tc.funcsDir(), config{})
				templates.AddFunc("upper", fn)

				var buf bytes.Buffer
				if err := templates.ExecuteTemplate(&buf, "funcs"+tc.ext, "", nil); err == nil {
					t.Fatalf("expected an error, got the output: %s", buf.String())
				}
			})
		}
	})
}

// The route function that Iris gives to an engine as "urlpath" has the shape
// func(string, ...any) string. Jet does not call that shape itself, the
// engine does, and a call with no argument is an error of the render.
func TestJetVariadicFunc(t *testing.T) {
	templates := views.Jet(nil, ".jet")
	templates.AddFunc("urlpath", func(name string, args ...any) string {
		return "/" + name
	})

	if err := templates.ParseTemplate("ok.jet", `{{ urlpath("users", 1) }}`); err != nil {
		t.Fatal(err)
	}
	if err := templates.ParseTemplate("bad.jet", `{{ urlpath() }}`); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, "ok.jet", "", nil); err != nil || buf.String() != "/users" {
		t.Fatalf("unexpected result: %q, %v", buf.String(), err)
	}
	if err := templates.ExecuteTemplate(&buf, "bad.jet", "", nil); err == nil {
		t.Fatal("expected an error for a call with no argument")
	}
}

// Jet collects its variables from maps the application owns: the data of
// view.WithData, which every request is given, and the variables of
// AddJetRuntimeVars. It must not write into them. Run with -race.
func TestJetSharedMaps(t *testing.T) {
	sharedData := iris.Map{"Name": "iris"}
	sharedVars := views.JetRuntimeVars{"user": reflect.ValueOf("makis")}

	templates := views.Jet(nil, ".jet")
	if err := templates.ParseTemplate("page.jet", `{{ user }} {{ extra }} {{ .Name }} {{ .Legacy }}`); err != nil {
		t.Fatal(err)
	}

	app := iris.New()
	app.Use(view.Handler(templates), view.WithData(sharedData))
	app.Get("/", func(ctx iris.Context) {
		views.AddJetRuntimeVars(ctx, sharedVars)
		views.AddJetRuntimeVars(ctx, views.JetRuntimeVars{"extra": reflect.ValueOf("more")})
		ctx.Values().Set("_jet", iris.Map{"Legacy": "legacy"})

		// Once with the data of the request and once with the map itself.
		layoutData := any(nil)
		if ctx.Query().GetString("direct") != "" {
			layoutData = sharedData
		}
		render("page.jet", "", layoutData)(ctx)
	})
	httptest.New(t, app)

	var wg sync.WaitGroup
	for i := range 8 {
		path := "/"
		if i%2 == 1 {
			path = "/?direct=1"
		}

		wg.Go(func() {
			for range 50 {
				if code, body := serve(app, path); code != http.StatusOK || body != "makis more iris legacy" {
					t.Errorf("%d: %s", code, body)
					return
				}
			}
		})
	}
	wg.Wait()

	if len(sharedData) != 1 || len(sharedVars) != 1 {
		t.Fatalf("the engine wrote into a map of the application: %v, %v", sharedData, sharedVars)
	}
}

// With Reload(true) a first load that fails is not the last word: the next
// render loads again.
func TestReloadAfterFailedFirstLoad(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		dir := copyDir(t, tc.dir())
		broken := filepath.Join(dir, "broken"+tc.ext)
		writeFile(t, broken, tc.broken)

		app := iris.New()
		app.Use(view.Handler(tc.new(dir, config{reload: true})))
		app.Get("/", render("index"+tc.ext, "", data))

		e := httptest.New(t, app)
		e.GET("/").Expect().Status(httptest.StatusInternalServerError)

		editFile(t, broken, tc.broken, "fixed")
		e.GET("/").Expect().Status(httptest.StatusOK).Body().Contains("page:index Hello iris")
	})
}

// A load that found nothing does not stop ParseTemplate from making the
// engine usable.
func TestParseTemplateAfterEmptyLoad(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		templates := tc.new(nil, config{})

		var buf bytes.Buffer
		if err := templates.ExecuteTemplate(&buf, "page"+tc.ext, "", data); !errors.Is(err, views.ErrNoTemplates) {
			t.Fatalf("expected ErrNoTemplates, got: %v", err)
		}

		if err := tc.parse(templates, "page"+tc.ext, "p text-template"); err != nil {
			t.Fatal(err)
		}

		if err := templates.ExecuteTemplate(&buf, "page"+tc.ext, "", data); err != nil {
			t.Fatal(err)
		}
		if got := buf.String(); !strings.Contains(got, "text-template") {
			t.Fatalf("unexpected output: %s", got)
		}
	})
}

// ParseTemplate on a loaded engine loads again with the new template. When
// that fails, for a file that stopped parsing in the meantime, it returns the
// error and the template is not kept.
func TestParseTemplateAddsOrFails(t *testing.T) {
	forEachEngine(t, func(t *testing.T, tc engineCase) {
		dir := copyDir(t, tc.dir())
		templates := tc.new(dir, config{})
		if err := templates.Load(); err != nil {
			t.Fatal(err)
		}

		broken := filepath.Join(dir, "broken"+tc.ext)
		writeFile(t, broken, tc.broken)

		if err := tc.parse(templates, "text"+tc.ext, "p text-template"); err == nil {
			t.Fatal("expected the error of the file that does not parse")
		}

		// The templates of the load before it still render.
		var buf bytes.Buffer
		if err := templates.ExecuteTemplate(&buf, "index"+tc.ext, "", data); err != nil {
			t.Fatal(err)
		}

		editFile(t, broken, tc.broken, "fixed")
		if err := templates.Load(); err != nil {
			t.Fatal(err)
		}

		err := templates.ExecuteTemplate(&buf, "text"+tc.ext, "", data)
		if _, ok := errors.AsType[view.ErrTemplateNotFound](err); !ok {
			t.Fatalf("expected the refused template to be unknown, got: %v", err)
		}
	})
}

// html/template has one function map for a set, so a function given to one
// text template is known to the ones that are added after it.
func TestHTMLParseTemplateSharesFuncs(t *testing.T) {
	templates := views.HTML(nil, ".html")

	err := templates.ParseTemplate("a.html", []byte(`a {{ shout "x" }}`), template.FuncMap{"shout": strings.ToUpper})
	if err != nil {
		t.Fatal(err)
	}
	if err = templates.ParseTemplate("b.html", []byte(`b {{ shout "y" }}`), nil); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err = templates.ExecuteTemplate(&buf, "b.html", "", nil); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "b Y" {
		t.Fatalf("unexpected output: %s", got)
	}
}

// A global helper that raymond would refuse must not cost the helper it was
// meant to replace.
func TestHandlebarsInvalidGlobalFunc(t *testing.T) {
	templates := views.Handlebars(nil, ".html")
	templates.AddGlobalFunc("views_test_kept", func() string { return "kept" })

	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected a panic for a helper that is not a function")
			}
		}()
		templates.AddGlobalFunc("views_test_kept", 42)
	}()

	if err := templates.ParseTemplate("page.html", `{{views_test_kept}}`, nil); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, "page.html", "", nil); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "kept" {
		t.Fatalf("unexpected output: %s", got)
	}
}

// A pongo2 set that the application assigns before the engine loads is the
// one the engine uses.
func TestDjangoOwnSet(t *testing.T) {
	own := pongo2.NewSet("own", pongo2.MustNewLocalFileSystemLoader(""))
	own.Globals["site"] = "example.com"

	templates := views.Django(nil, ".html")
	templates.Set = own

	if err := templates.ParseTemplate("page.html", []byte(`{{ site }}`)); err != nil {
		t.Fatal(err)
	}
	if templates.Set != own {
		t.Fatal("the engine replaced the set of the application")
	}

	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, "page.html", "", nil); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "example.com" {
		t.Fatalf("unexpected output: %s", got)
	}
}
