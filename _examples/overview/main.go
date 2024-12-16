package main

import (
	"fmt"
	"html/template"
	"time"

	"github.com/kataras/iris/v14"
	"github.com/kataras/iris/v14/middleware/compress"
	"github.com/kataras/iris/v14/middleware/view"

	"github.com/iris-contrib/views"
)

// ViewFunctions presents some builtin functions
// for html view engines. See `View.Funcs` or `view/html.Funcs` and etc.
var Functions = template.FuncMap{
	"Now": time.Now,
}

func main() {
	app := iris.New()
	app.UseRouter(compress.Handler)

	templates := views.HTML("./templates", ".html").
		Funcs(Functions). // Optionally register some more builtin functions.
		Reload(false)     // Set Reload to true on development.
	if err := templates.Load(); err != nil {
		panic(err)
	}
	app.UseRouter(view.Handler(templates))

	app.Get("/", func(ctx iris.Context) {
		// the .Name inside the ./templates/hi.html.
		// view.KeyValue(ctx, "Name", "iris")
		// err := view.Render(ctx, "hi.html", "", nil);
		// OR:
		// Context, template name, layout filename (optional), data (optional).
		err := view.Render(ctx, "hi.html", "", iris.Map{"Name": "iris"})
		if err != nil {
			ctx.HTML(fmt.Sprintf("<h3>%s</h3>", err.Error()))
			return
		}
	})

	app.Get("/example_map", func(ctx iris.Context) {
		examplePage := iris.Map{
			"Name":   "Example Name",
			"Age":    42,
			"Items":  []string{"Example slice entry 1", "entry 2", "entry 3"},
			"Map":    iris.Map{"map key": "map value", "other key": "other value"},
			"Nested": iris.Map{"Title": "Iris E-Book", "Pages": 620},
		}

		if err := view.Render(ctx, "example.html", "", examplePage); err != nil {
			ctx.HTML(fmt.Sprintf("<h3>%s</h3>", err.Error()))
			return
		}
	})

	app.Get("/example_struct", func(ctx iris.Context) {
		type book struct {
			Title string
			Pages int
		}

		var examplePage = struct {
			Name   string
			Age    int
			Items  []string
			Map    map[string]any
			Nested book
		}{
			"Example Name",
			42,
			[]string{"Example slice entry 1", "entry 2", "entry 3"},
			iris.Map{"map key": "map value", "other key": "other value"},
			book{
				"Iris E-Book",
				620,
			},
		}

		if err := view.Render(ctx, "example.html", "", examplePage); err != nil {
			ctx.HTML(fmt.Sprintf("<h3>%s</h3>", err.Error()))
			return
		}
	})

	app.Get("/functions", func(ctx iris.Context) {
		var functionsPage = struct {
			// A function.
			Now func() time.Time
			// A struct field which contains methods.
			Ctx iris.Context
		}{
			Now: time.Now,
			Ctx: ctx,
		}

		if err := view.Render(ctx, "functions.html", "", functionsPage); err != nil {
			ctx.HTML(fmt.Sprintf("<h3>%s</h3>", err.Error()))
			return
		}
	})

	// http://localhost:8080/
	app.Listen(":8080")
}
