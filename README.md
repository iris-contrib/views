# View

Iris supports 7 template engines out-of-the-box, developers can still use any external golang template engine,
as `Context` and `Context.ResponseWriter()` completes the `io.Writer` interface. Integration with the [Iris View](https://github.com/kataras/iris/blob/main/middleware/view/README.md) middleware.

All template engines share a common API.
Parse using embedded assets, Layouts and Router-specific layout, Template Funcs, Partial Render and more.

| #  | Name       | Parser   |
|:---|:-----------|----------|
| 1 | HTML       | [html/template](https://pkg.go.dev/html/template) |
| 2 | Blocks     | [kataras/blocks](https://github.com/kataras/blocks) |
| 3 | Django     | [flosch/pongo2](https://github.com/flosch/pongo2) |
| 4 | Pug        | [Joker/jade](https://github.com/Joker/jade) |
| 5 | Handlebars | [mailgun/raymond](https://github.com/mailgun/raymond) |
| 6 | Jet        | [CloudyKit/jet](https://github.com/CloudyKit/jet) |
| 7 | Ace        | [yosssi/ace](https://github.com/yosssi/ace) |

[List of Examples](https://github.com/iris-contrib/views/tree/main/_examples).

You can serve [quicktemplate](https://github.com/valyala/quicktemplate) files too, take a look at the [_examples/quicktemplate](https://github.com/iris-contrib/views/tree/main/_examples/quicktemplate) example.

## Overview

```go
// file: main.go
package main

import (
    "github.com/kataras/iris/v14"
    "github.com/kataras/iris/v14/middlweare/view" 

    "github.com/iris-contrib/views"
)

func main() {
    app := iris.New()
    // Load all templates from the "./views" folder
    // where extension is ".html" and parse them
    // using the standard `html/template` package.
    templates := views.HTML("./views", ".html")
    templates.Load()
    app.UseRouter(view.Handler(templates))

    // Method:    GET
    // Resource:  http://localhost:8080
    app.Get("/", func(ctx iris.Context) {
        // Render template file: ./views/hello.html with data.
        err := view.Render(ctx, "hello.html", "layouts/main.html", iris.Map{"message": "Hello world!"})
        // OR Bind: {{.message}} with "Hello world!" on any handler of the chain:
        // view.KeyValue(ctx, "message", "Hello world!")
        // And render the template file: ./views/hello.html
        // err := view.Render(ctx, "hello.html", "layouts/main.html", nil)
        if err != nil {
            ctx.HTML(fmt.Sprintf("<h3>%s</h3>", err.Error()))
            return
        }
    })

    // Method:    GET
    // Resource:  http://localhost:8080/user/42
    app.Get("/user/{id:int64}", func(ctx iris.Context) {
        userID := ctx.Params().GetInt64("id")
        ctx.Writef("User ID: %d", userID)
    })

    // Start the server using a network address.
    app.Listen(":8080")
}
```

```html
<!-- file: ./views/layouts/main.html -->
<!DOCTYPE html>
<head>
    <title>Page Title</title>
</head>
<body>
    {{ yield . }}
    <footer>
        {{ render "partials/footer.html" . }}
    </footer>
</body>
</html>
```

```html
<!-- file: ./views/partials/footer.html -->
<h3>Footer Partial</h3>
```

```html
<!-- file: ./views/hello.html -->
<html>
<head>
    <title>Hello Page</title>
</head>
<body>
    <h1>{{ .message }}</h1>
</body>
</html>
```

## Template functions

```go
templates := views.HTML("./templates", ".html")
// register a custom template func.
templates.AddFunc("greet", func(s string) string {
    return "Greetings " + s + "!"
})
templates.Load()
// [...]
```

```html
<b>{{ greet "kataras" }}</b> <!-- will be rendered as: <b>Greetings kataras!</b> -->
```

## Embedded

View engine supports bundled(https://github.com/go-bindata/go-bindata) template files too. Latest
`go-bindata` release gives you a compatible `http.FileSystem` that can be provided as the first argument of a view engine's initialization, e.g. `HTML(AssetFile(), ".html")`.


```sh
$ go install github.com/go-bindata/go-bindata/v3/go-bindata@latest
$ go-bindata -fs -prefix "templates" ./templates/...
$ go run .
```

Usage:

```go
templates := views.HTML(AssetFile(), ".html")
templates.Load()
// [...]
```

Examples can be found here: https://github.com/iris-contrib/views/tree/main/_examples/view/embedding-templates-into-app and  https://github.com/iris-contrib/views/tree/main/_examples/view/embedding-templates-into-app-bindata.

## Reload

Enable auto-reloading of templates on each request. Useful while developers are in dev mode
as they no neeed to restart their app on every template edit.

Example code:

```go
templates := views.Pug("./templates", ".jade")
templates.Reload(true) // <--- set to true to re-build the templates on each request.
templates.Load()
app.UseRouter(view.Handler(templates))
// [...]
```
