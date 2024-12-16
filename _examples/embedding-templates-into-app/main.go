package main

import (
	"embed"
	"fmt"

	"github.com/kataras/iris/v14"
	"github.com/kataras/iris/v14/middleware/view"

	"github.com/iris-contrib/views"
)

//go:embed embedded/*
var embeddedFS embed.FS

func main() {
	app := iris.New()

	templates := views.HTML(embeddedFS, ".html").RootDir("embedded/templates")
	templates.Layout("layouts/layout.html")
	templates.AddFunc("greet", func(s string) string {
		return "Greetings " + s + "!"
	})
	if err := templates.Load(); err != nil {
		panic(err)
	}
	app.UseRouter(view.Handler(templates))

	app.Get("/", func(ctx iris.Context) {
		if err := view.Render(ctx, "page1.html", "", nil); err != nil {
			ctx.HTML(fmt.Sprintf("<h3>%s</h3>", err.Error()))
			return
		}
	})

	// remove the layout for a specific route
	app.Get("/nolayout", func(ctx iris.Context) {
		view.Layout(ctx, views.NoLayout)
		if err := view.Render(ctx, "page1.html", "", nil); err != nil {
			ctx.HTML(fmt.Sprintf("<h3>%s</h3>", err.Error()))
			return
		}
	})

	// set a layout for a router, .Layout should be BEFORE any Get or other Handle router's method
	my := app.Router("/my", view.WithLayout("layouts/mylayout.html"))
	{ // both of these will use the layouts/mylayout.html as their layout.
		my.Get("/", func(ctx iris.Context) {
			if err := view.Render(ctx, "page1.html", "", nil); err != nil {
				ctx.HTML(fmt.Sprintf("<h3>%s</h3>", err.Error()))
				return
			}
		})
		my.Get("/other", func(ctx iris.Context) {
			if err := view.Render(ctx, "page1.html", "", nil); err != nil {
				ctx.HTML(fmt.Sprintf("<h3>%s</h3>", err.Error()))
				return
			}
		})
	}

	// http://localhost:8080
	// http://localhost:8080/nolayout
	// http://localhost:8080/my
	// http://localhost:8080/my/other
	app.Listen(":8080")
}
