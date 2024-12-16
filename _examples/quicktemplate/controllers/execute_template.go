package controllers

import (
	"github.com/iris-contrib/views/_examples/quicktemplate/templates"

	"github.com/kataras/iris/v14"
)

// ExecuteTemplate renders a "tmpl" partial template to the `Context.ResponseWriter`.
func ExecuteTemplate(ctx iris.Context, tmpl templates.Partial) {
	ctx.SetContentType("text/html")
	templates.WriteTemplate(ctx, tmpl)
}
