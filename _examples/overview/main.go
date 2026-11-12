// The HTML engine: a directory of templates, rendered through the view middleware.
//
// views.HTML reads every .html file under ./templates and parses it with the
// standard html/template package. app.Use(view.Handler(templates)) stores the
// engine on each request, and a handler renders with
// view.Render(ctx, name, layout, data).
//
// There is no Load call: an engine that was never loaded parses its templates
// on the first render. Call templates.Load() at startup when the program
// should refuse to start with a template that does not parse, as
// ../embedding-templates-into-app does.
//
// The data of a template is a map or a struct. A function that templates call
// is given to the engine before it loads.
//
// Learn: register an engine of this package, render a template with a map and
// with a struct, and give the templates a function of your own.
//
// Run, from this directory, because the templates are read from ./templates:
//
//	go run .
//
// Try:
//
//	curl localhost:8080/                 # 200, hi.html
//	curl localhost:8080/example_map      # 200, example.html with a map
//	curl localhost:8080/example_struct   # 200, the same page with a struct
//	curl localhost:8080/functions        # 200, a template that calls functions
//
// Next: ../embedding-templates-into-app
package main

import (
	"html/template"
	"strings"
	"time"

	"github.com/iris-contrib/views"

	"github.com/kataras/iris/v14"
	"github.com/kataras/iris/v14/middleware/view"
	"github.com/kataras/iris/v14/rest/errors"
)

// functions are the functions the templates may call, next to the ones
// html/template has built in.
var functions = template.FuncMap{
	"now":   func() string { return time.Now().Format(time.RFC1123) },
	"upper": strings.ToUpper,
}

type book struct {
	Title string
	Pages int
}

// examplePage is the data of example.html as a struct.
// The template reads the same names from a map.
type examplePage struct {
	Name   string
	Age    int
	Items  []string
	Map    map[string]any
	Nested book
}

// Initials is a method, which a template calls the way it reads a field.
func (p examplePage) Initials() string {
	var initials strings.Builder
	for word := range strings.FieldsSeq(p.Name) {
		initials.WriteString(word[:1])
	}
	return initials.String()
}

func main() {
	app := iris.New()

	templates := views.HTML("./templates", ".html").
		Funcs(functions). // before the engine loads: html/template resolves names when it parses.
		Reload(false)     // true parses the templates again on every render, for development.

	app.Use(view.Handler(templates))

	app.Get("/", index)
	app.Get("/example_map", exampleMap)
	app.Get("/example_struct", exampleStruct)
	app.Get("/functions", functionsPage)

	app.Listen(":8080")
}

func index(ctx iris.Context) {
	// {{ .Name }} inside ./templates/hi.html.
	// The arguments: the template, an optional layout and the data.
	render(ctx, "hi.html", iris.Map{"Name": "iris"})
}

func exampleMap(ctx iris.Context) {
	render(ctx, "example.html", iris.Map{
		"Name":   "Example Name",
		"Age":    42,
		"Items":  []string{"Example slice entry 1", "entry 2", "entry 3"},
		"Map":    iris.Map{"map key": "map value", "other key": "other value"},
		"Nested": iris.Map{"Title": "Iris E-Book", "Pages": 620},
	})
}

func exampleStruct(ctx iris.Context) {
	render(ctx, "example.html", examplePage{
		Name:   "Example Name",
		Age:    42,
		Items:  []string{"Example slice entry 1", "entry 2", "entry 3"},
		Map:    iris.Map{"map key": "map value", "other key": "other value"},
		Nested: book{Title: "Iris E-Book", Pages: 620},
	})
}

func functionsPage(ctx iris.Context) {
	render(ctx, "functions.html", examplePage{Name: "Example Name"})
}

// render writes a template, or a generic 500 when that fails. The error of a
// template names files, lines and data fields, so it is logged and not sent.
func render(ctx iris.Context, name string, data any) {
	if err := view.Render(ctx, name, "", data); err != nil {
		errors.Internal.LogErr(ctx, err)
	}
}
