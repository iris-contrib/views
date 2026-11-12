// HTML from github.com/a-h/templ, components written in a .templ file and
// compiled to Go code.
//
// templ needs no engine of the views package and no view middleware. A
// component is a value with a Render method, and Iris renders one in two
// ways: iris.Component(component) is a whole handler, and
// ctx.RenderComponent(component) renders from inside a handler, which is the
// form to use when the component depends on the request. hello.templ and
// signup.templ are the sources; the generated *_templ.go files are
// committed, so the example runs as it is.
//
// It is a module of its own, so that templ is not a dependency of the views
// package.
//
// Learn: serve templ components, as a handler and from a handler.
//
// Run:
//
//	go run .
//
// Try:
//
//	curl localhost:8080/              # 200, <div>Hello, <strong>Makis</strong></div>
//	curl localhost:8080/hello/Maria   # 200, the same component for the name in the path
//	curl localhost:8080/signup        # 200, a page with a form
//
// After a change to a .templ file, generate the Go code again:
//
//	go install github.com/a-h/templ/cmd/templ@v0.2.793
//	templ generate
//
// The version is the one in go.mod: the generated code and the templ runtime
// have to match.
package main

import (
	"github.com/kataras/iris/v14"
)

func main() {
	app := iris.New()

	// A component that is the same for every request is a handler by itself.
	app.Get("/", iris.Component(hello("Makis")))
	app.Get("/signup", iris.Component(signup()))

	// One that depends on the request is built and rendered in a handler.
	app.Get("/hello/{name}", func(ctx iris.Context) {
		ctx.RenderComponent(hello(ctx.Params().GetString("name")))
	})

	app.Listen(":8080")
}
