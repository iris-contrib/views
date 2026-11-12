// Templates compiled into the binary, with layouts and a partial.
//
// The files under embedded/templates are embedded with //go:embed, so the
// program runs from any directory and ships as one file. RootDir points the
// engine at that directory inside the embedded tree. Load is called at
// startup: a template that does not parse stops the program there, not on
// the first request.
//
// A layout prints the page where it says {{ yield . }}. The layout of a
// render is the first of these that is set:
//
//  1. the layout argument of view.Render,
//  2. the layout of the request: view.WithLayout for a group of routes,
//     view.Layout inside a handler,
//  3. the engine's own default, Layout.
//
// views.NoLayout in the first or the second place renders the page alone.
//
// Learn: embed templates, load them at startup, and choose a layout for the
// whole engine, for a group of routes and for one render.
//
// Run:
//
//	go run .
//
// Try:
//
//	curl localhost:8080/           # 200, page1 inside layouts/layout.html, the engine's default
//	curl localhost:8080/nolayout   # 200, page1 alone
//	curl localhost:8080/my         # 200, page1 inside layouts/mylayout.html
//	curl localhost:8080/my/other   # 200, the same layout, set once for the group
//
// Next: ../write-to
package main

import (
	"embed"
	"log"

	"github.com/iris-contrib/views"

	"github.com/kataras/iris/v14"
	"github.com/kataras/iris/v14/middleware/view"
	"github.com/kataras/iris/v14/rest/errors"
)

// embedded/templates/page1.html, its partial and the two layouts.
//
//go:embed embedded
var embeddedFS embed.FS

func main() {
	templates := views.HTML(embeddedFS, ".html").RootDir("embedded/templates")
	templates.Layout("layouts/layout.html")
	// page1.html calls it: {{ greet "iris developer" }}.
	templates.AddFunc("greet", func(s string) string {
		return "Greetings " + s + "!"
	})

	if err := templates.Load(); err != nil {
		log.Fatal(err)
	}

	app := iris.New()
	app.Use(view.Handler(templates))

	app.Get("/", page)

	// No layout for one route.
	app.Get("/nolayout", func(ctx iris.Context) {
		view.Layout(ctx, views.NoLayout)
		page(ctx)
	})

	// One layout for a group of routes. The engine is the application's,
	// only the layout differs.
	my := app.Router("/my", view.WithLayout("layouts/mylayout.html"))
	my.Get("/", page)
	my.Get("/other", page)

	app.Listen(":8080")
}

func page(ctx iris.Context) {
	// An empty layout argument: the layout comes from the request or,
	// when the request has none, from the engine.
	if err := view.Render(ctx, "page1.html", "", nil); err != nil {
		errors.Internal.LogErr(ctx, err) // a generic 500; the template error is logged, not sent.
	}
}
