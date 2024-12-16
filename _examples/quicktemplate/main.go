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
	// http://localhost:8080
	// http://localhost:8080/yourname
	app.Listen(":8080")
}
