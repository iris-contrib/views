package controllers

import (
	"github.com/iris-contrib/views/_examples/quicktemplate/templates"

	"github.com/kataras/iris/v14"
)

// Index renders our ../templates/index.qtpl file using the compiled ../templates/index.qtpl.go file.
func Index(ctx iris.Context) {
	tmpl := &templates.Index{}

	// render the template
	ExecuteTemplate(ctx, tmpl)
}
