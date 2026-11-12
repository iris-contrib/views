// HTML from github.com/valyala/quicktemplate, a template engine that compiles
// its templates to Go code.
//
// quicktemplate needs no engine of the views package and no view middleware:
// the code it generates writes into an io.Writer, and the Iris Context is an
// io.Writer. templates/base.qtpl is the page every response shares and
// declares the Partial interface. index.qtpl and hello.qtpl implement it, and
// a handler passes one of them to templates.WriteTemplate. The generated
// *.qtpl.go files are committed, so the example runs as it is.
//
// It is a module of its own, so that quicktemplate is not a dependency of
// the views package.
//
// Learn: serve the output of a compiled template engine from a handler and
// test the response with httptest.
//
// Run:
//
//	go run .
//
// Try:
//
//	curl localhost:8080/           # 200, the index page
//	curl localhost:8080/yourname   # 200, "Hello yourname!"
//	go test .                      # the same two requests, in memory
//
// After a change to a .qtpl file, generate the Go code again:
//
//	go install github.com/valyala/quicktemplate/qtc@latest
//	qtc -dir=./templates
//
// Next: ../templ
package main

import (
	"github.com/iris-contrib/views/_examples/quicktemplate/controllers"

	"github.com/kataras/iris/v14"
)

func newApp() *iris.Application {
	app := iris.New()
	app.Get("/", controllers.Index)
	app.Get("/{name}", controllers.Hello)

	return app
}

func main() {
	app := newApp()
	app.Listen(":8080")
}
