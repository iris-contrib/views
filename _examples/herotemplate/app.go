// HTML from github.com/shiyanhui/hero, a template engine that compiles its
// templates to Go code.
//
// hero needs no engine of the views package and no view middleware: the
// functions it generates write HTML into a bytes.Buffer or into an io.Writer,
// and the Iris Context is an io.Writer. The files under ./template are the
// templates and the Go code generated from them, which is committed, so the
// example runs as it is.
//
// It is a module of its own, so that hero is not a dependency of the views
// package.
//
// Learn: serve the output of a compiled template engine from a handler,
// through a buffer and by writing straight to the response.
//
// Run:
//
//	go run .
//
// Try:
//
//	curl localhost:8080/users          # 200, the list, rendered into a buffer first
//	curl localhost:8080/users/writer   # 200, the same list, written straight to the response
//
// After a change to a template, generate the Go code again:
//
//	go install github.com/shiyanhui/hero/hero@latest
//	hero -source=./template
//
// Next: ../quicktemplate
package main

import (
	"bytes"

	"github.com/iris-contrib/views/_examples/herotemplate/template"

	"github.com/kataras/iris/v14"
	"github.com/kataras/iris/v14/rest/errors"
)

var userList = []string{
	"Alice",
	"Bob",
	"Tom",
}

func main() {
	app := iris.New()

	app.Get("/users", users)
	app.Get("/users/writer", usersToWriter)

	app.Listen(":8080")
}

// users renders into a buffer, so nothing is sent when the template fails.
// hero has a pool of buffers for this, hero.GetBuffer and hero.PutBuffer.
func users(ctx iris.Context) {
	buffer := new(bytes.Buffer)
	template.UserList(userList, buffer)

	ctx.HTML(buffer.String())
}

// usersToWriter gives the Context itself to the template.
func usersToWriter(ctx iris.Context) {
	ctx.SetContentType("text/html")

	if _, err := template.UserListToWriter(userList, ctx); err != nil {
		errors.Internal.LogErr(ctx, err)
	}
}
