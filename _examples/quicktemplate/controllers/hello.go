package controllers

import (
	"github.com/iris-contrib/views/_examples/quicktemplate/templates"

	"github.com/kataras/iris/v14"
)

// Hello renders our ../templates/hello.qtpl file using the compiled ../templates/hello.qtpl.go file.
func Hello(ctx iris.Context) {
	vars := make(map[string]any)
	vars["message"] = "Hello World!"
	vars["name"] = ctx.Params().GetString("name")
	// [...]
	// &templates.Hello{ Vars: vars }
	// [...]
	// set view data to the `Vars` template's field
	tmpl := &templates.Hello{
		Vars: vars,
	}

	// render the template
	ExecuteTemplate(ctx, tmpl)
}
