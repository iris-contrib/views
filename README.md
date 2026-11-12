# Views

[![CI](https://github.com/iris-contrib/views/actions/workflows/ci.yml/badge.svg)](https://github.com/iris-contrib/views/actions/workflows/ci.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/iris-contrib/views.svg)](https://pkg.go.dev/github.com/iris-contrib/views)

The template engines of [Iris](https://github.com/kataras/iris) v14.

Since v14 the framework keeps only the contract, in its [view middleware](https://github.com/kataras/iris/tree/main/middleware/view), and the engines live in this repository. An engine wraps one template parser. All six satisfy `view.Engine`, `view.EngineFuncer` and `view.EngineExtenser`, and all six are registered and rendered the same way.

| Engine | Constructor | Parser | Layout argument |
|:-------|:------------|:-------|:----------------|
| HTML | `views.HTML` | [html/template](https://pkg.go.dev/html/template) | yes |
| Django | `views.Django` | [flosch/pongo2](https://github.com/flosch/pongo2) | no, a template says `{% extends %}` |
| Handlebars | `views.Handlebars` | [mailgun/raymond](https://github.com/mailgun/raymond) | yes, with map data |
| Jet | `views.Jet` | [CloudyKit/jet](https://github.com/CloudyKit/jet) | no, a template says `{{ extends }}` |
| Pug | `views.Pug` | [Joker/jade](https://github.com/Joker/jade) | yes |
| Ace | `views.Ace` | [yosssi/ace](https://github.com/yosssi/ace) | yes |

The Blocks engine of Iris v12 is not here and is not needed: [kataras/blocks](https://github.com/kataras/blocks) satisfies `view.Engine` by itself. Pass a `blocks.New("./views")` to `view.Handler`.

## Install

```sh
go get github.com/iris-contrib/views@latest
```

It needs Go 1.27 or newer, as Iris v14 does.

## Quick start

```go
package main

import (
	"github.com/iris-contrib/views"

	"github.com/kataras/iris/v14"
	"github.com/kataras/iris/v14/middleware/view"
	"github.com/kataras/iris/v14/rest/errors"
)

func main() {
	app := iris.New()

	// Every .html file under ./views, parsed with html/template.
	templates := views.HTML("./views", ".html")
	app.Use(view.Handler(templates))

	app.Get("/", index)

	app.Listen(":8080")
}

func index(ctx iris.Context) {
	data := iris.Map{"Message": "Hello world!"}

	// The template, the layout ("" for none) and the data.
	if err := view.Render(ctx, "hello.html", "layouts/main.html", data); err != nil {
		// A generic 500. The error of a template names files, lines and
		// data fields, so it is logged and not sent.
		errors.Internal.LogErr(ctx, err)
	}
}
```

```html
<!-- ./views/layouts/main.html -->
<!DOCTYPE html>
<html>
<head>
    <title>Page Title</title>
</head>
<body>
    {{ yield . }}
    <footer>{{ render "partials/footer.html" . }}</footer>
</body>
</html>
```

```html
<!-- ./views/hello.html -->
<h1>{{ .Message }}</h1>
```

```html
<!-- ./views/partials/footer.html -->
<p>Footer partial</p>
```

## Registering an engine

`view.Handler(engine)` stores the engine on the request. Register it once for the application, or give a group of routes an engine of its own:

```go
func routes(app *iris.Application) {
	// For the application.
	app.Use(view.Handler(views.HTML("./views", ".html")))

	// For the routes under /admin, which render Django templates.
	admin := app.Router("/admin", view.Handler(views.Django("./views/admin", ".html")))
	admin.Get("/", func(ctx iris.Context) {
		view.Render(ctx, "dashboard.html", "", iris.Map{"Title": "Dashboard"})
	})
}
```

Any number of engines can live in one program, two of the same kind included.

## Rendering

`view.Render(ctx, name, layout, data)` renders a template into the response. `view.WithTemplate` is the same thing as a handler:

```go
func about(app *iris.Application) {
	app.Get("/about", view.WithTemplate(&view.Template{
		Name:   "about.html",
		Layout: "layouts/main.html",
		Data:   iris.Map{"Title": "About"},
	}))
}
```

A name is the path of the file below the template directory, with forward slashes. The extension may be left out and a leading slash is ignored, so `"users/list.html"`, `"users/list"` and `"/users/list.html"` name the same template.

Errors:

- A template or a layout that does not exist is a `view.ErrTemplateNotFound`. Its `Name` is the name that was asked for and `IsLayout` says which of the two is missing.
- `view.ErrNoEngine` means that no `view.Handler` ran for the request.
- Anything else comes from the parser: a template that does not parse, a function that failed.

An engine also renders into any `io.Writer`, with no request around it. `ExecuteTemplate` is the method `view.Render` calls:

```go
func welcomeMail(templates *views.HTMLEngine, name string) (string, error) {
	var body strings.Builder
	err := templates.ExecuteTemplate(&body, "email/welcome.html", "layouts/email.html", map[string]any{"Name": name})
	return body.String(), err
}
```

## Loading

The six engines load their templates by the same rule.

1. `Load()` parses every template and returns the first error. Call it at startup when the program should not start with a template that does not parse.
2. An engine that was never loaded loads itself on its first render. Requests that arrive together wait for that one load. This is why the two lines of the quick start are enough.
3. `Reload(true)` parses the templates again on every render. Use it while you edit templates, not in production.

```go
func newTemplates() *views.HTMLEngine {
	templates := views.HTML("./views", ".html")
	if err := templates.Load(); err != nil {
		log.Fatal(err)
	}
	return templates
}
```

When the first load happens inside a request and fails, that request gets the error and so does every request after it, until a `Load()` succeeds. It is not tried again on each request. `Load()` may be called again at any time to parse everything anew; if it fails then, the templates of the previous load keep rendering.

A load that finds nothing returns `views.ErrNoTemplates`. A wrong directory and a wrong extension are the usual causes, and the error of a directory that does not exist names it.

A template that includes, extends or renders itself is an error too, not a crash, with one exception that is described under [Jet](#jet).

## Where the templates come from

The first argument of a constructor is a directory name, an `fs.FS` or an `http.FileSystem`.

```go
//go:embed views
var viewsFS embed.FS

func embedded() *views.HTMLEngine {
	// RootDir is the directory inside the embedded tree.
	return views.HTML(viewsFS, ".html").RootDir("views")
}
```

A directory name is relative to the working directory of the process, so embedded templates are the safer choice for a program that is deployed as one binary.

Templates can also be given as text, alone (pass `nil` as the first argument) or next to the files:

```go
func fromText() (*views.HTMLEngine, error) {
	templates := views.HTML(nil, ".html")
	err := templates.ParseTemplate("hello.html", []byte(`<h1>Hello {{ .Name }}</h1>`), nil)
	return templates, err
}
```

`ParseTemplate` returns the error of a template that does not parse, and adds nothing then. On an engine that is loaded already it loads again, so the new template renders at once. Every engine has it; the arguments differ a little, see each engine's documentation.

## Layouts

HTML, Pug, Ace and Handlebars render a page inside a layout. The layout of a render is the first of these that is set:

1. the layout argument of `view.Render`, or the `Layout` field of a `view.Template`,
2. the layout of the request, set by `view.WithLayout` for a group of routes or by `view.Layout` inside a handler,
3. the default of the engine, set by its `Layout` method.

`views.NoLayout`, in the first or the second place, renders the page alone.

```go
func layouts(app *iris.Application) {
	templates := views.HTML("./views", ".html").Layout("layouts/main.html")
	app.Use(view.Handler(templates))

	// layouts/main.html, the default of the engine.
	app.Get("/", func(ctx iris.Context) {
		view.Render(ctx, "index.html", "", nil)
	})

	// layouts/admin.html for every route of the group.
	admin := app.Router("/admin", view.WithLayout("layouts/admin.html"))
	admin.Get("/", func(ctx iris.Context) {
		view.Render(ctx, "admin/index.html", "", nil)
	})

	// No layout for this render.
	app.Get("/print", func(ctx iris.Context) {
		view.Render(ctx, "index.html", views.NoLayout, nil)
	})
}
```

Django and Jet ignore the layout, wherever it comes from. Their templates name their own parent, which is how those two languages work.

## Data

The data of a template is the last argument of `view.Render`. When it is `nil`, the template gets the data of the request, which `view.WithData`, `view.Data` and `view.KeyValue` set:

```go
func data(app *iris.Application) {
	app.Get("/profile", func(ctx iris.Context) {
		view.KeyValue(ctx, "User", "maria")
		view.KeyValue(ctx, "Role", "editor")

		// nil: the template reads User and Role.
		view.Render(ctx, "profile.html", "", nil)
	})
}
```

A map and a struct both work as data in every engine, with two limits. Django accepts nothing else. Handlebars needs a `map[string]any` or `nil` when it renders through a layout, because the page reaches the layout as one more entry of that map.

`view.WithData(m)` gives the same map `m` to every request. `view.KeyValue` does not write into it: the first call of a request copies the map, so a key added for one request is not seen by another. The engines do not write into the data either.

## Functions

Give a template function to the engine with its `AddFunc` method:

```go
func withFuncs() *views.HTMLEngine {
	templates := views.HTML("./views", ".html")
	templates.AddFunc("greet", func(name string) string {
		return "Greetings " + name + "!"
	})
	return templates
}
```

```html
<b>{{ greet "kataras" }}</b> <!-- <b>Greetings kataras!</b> -->
```

`view.AddFunc(ctx, name, fn)` and `view.WithFuncMap(funcs)` reach the same method through the request. The function still belongs to the engine and not to the request, so it must not depend on the request it was added from. A function that does depend on the request goes into the data of the render. In HTML, Pug and Ace a template calls it with `{{ call .Greet .Name }}`.

When the function takes effect depends on the engine:

- Django and Jet look a function up when a template runs. It works at once.
- HTML, Pug, Ace and Handlebars bind functions when they parse, so the function takes effect at the next load. Add it before `Load()`. With an engine that loads itself, a `view.WithFuncMap` registered ahead of the handlers is in time, because the first render is what loads the engine.

A value that the parser does not accept as a function fails the load with an error.

## The engines

### HTML

`views.HTML(dirOrFS, ".html")` parses with the standard `html/template` package, so everything in [its documentation](https://pkg.go.dev/html/template) applies, contextual escaping included.

```go
func html() *views.HTMLEngine {
	return views.HTML("./views", ".html").
		Layout("layouts/main.html").
		Option("missingkey=error").
		Funcs(template.FuncMap{"upper": strings.ToUpper})
}
```

The engine adds these actions to the ones of `html/template`:

| Action | Renders |
|:-------|:--------|
| `{{ yield . }}` | the page, inside its layout. Nothing anywhere else. |
| `{{ render "partials/footer.html" . }}` | any template, by name |
| `{{ partial_r ".script" . }}` | for the page `users/index.html`, the template `users/index.script.html`, if it exists |
| `{{ part "head" . }}` | for the page `users/index.html`, the template `users/index-head`, if a `{{ define }}` made one |
| `{{ partial "sidebar" . }}` | for the page `users/index.html`, the template `sidebar-users/index.html`, if it exists |
| `{{ current }}` | the name of the page |

`Delims` changes the `{{` and `}}` delimiters, `Option` passes options such as `missingkey=error` to the parser, and `Funcs`, `SetFuncs`, `AddFunc` and `AddLayoutFunc` add functions.

Two things to know:

- `html/template` keeps one function map for a whole set of templates, and `yield` has to know which page it is rendering. The engine therefore renders in copies of the set, one render per copy at a time, and makes a new copy only when all of them are busy. It keeps one per CPU at most. Renders that overlap beyond that, because a template function waits for a query for example, get a copy that the garbage collector takes back afterwards. A copy costs roughly three times the size of the template sources in memory.
- A page is rendered into a buffer and sent when it is complete. When a template fails halfway, nothing has been written and the handler can still answer with an error page.

### Django

`views.Django(dirOrFS, ".html")` parses with pongo2, a Go implementation of the Django template language.

```go
func django() *views.DjangoEngine {
	templates := views.Django("./views", ".html")

	// A global: a function or a plain value.
	templates.AddFunc("site", "example.com")

	// A filter: {{ title|shout }}.
	templates.AddFilter("shout", func(in *views.Value, param *views.Value) (*views.Value, *views.Error) {
		return views.AsValue(strings.ToUpper(in.String()) + "!"), nil
	})

	return templates
}
```

```django
{# ./views/page.html #}
{% extends "layouts/main.html" %}

{% block content %}
<h1>{{ title|shout }}</h1>
{% include "partials/footer.html" %}
{% endblock %}
```

- The layout argument is ignored. A template names its parent with `{% extends %}`.
- The names in `{% extends %}` and `{% include %}` are relative to the template directory, whichever file they are written in.
- The data is a `map[string]any` or a struct. Anything else is an error.
- Filters and tags are kept by pongo2 for the whole process. All Django engines of a program share them, and a filter that is registered again under the same name replaces the earlier one. Register them at startup.

### Handlebars

`views.Handlebars(dirOrFS, ".html")` parses with raymond.

```go
func handlebars() *views.HandlebarsEngine {
	templates := views.Handlebars("./views", ".html").Layout("layouts/main.html")

	// A helper: {{ fullName author }}.
	templates.AddFunc("fullName", func(person map[string]string) string {
		return person["firstName"] + " " + person["lastName"]
	})

	return templates
}
```

```handlebars
{{! ./views/layouts/main.html }}
<html>
<body>
    {{ yield }}
    <footer>{{ render "partials/footer.html" . }}</footer>
</body>
</html>
```

- A layout prints the page with `{{ yield }}`. The page is one more entry of the data, which is why a render through a layout needs a `map[string]any` or `nil`.
- `{{ render "name" . }}` renders another template. A name that does not exist fails the render.
- A helper added with `AddFunc` belongs to the engine. `AddGlobalFunc` registers one for every Handlebars engine of the process, replaces a helper of the same name, and panics for a value raymond would not take.
- A helper returns one value. raymond does not accept a function with a second, error result.

### Jet

`views.Jet(dirOrFS, ".jet")` parses with jet. The extension must be `.jet`, `.html.jet` or `.jet.html`, and the constructor panics for any other.

```go
func jet() *views.JetEngine {
	templates := views.Jet("./views", ".jet")

	// Any Go function: {{ upper(.Title) }}.
	templates.AddFunc("upper", strings.ToUpper)
	// A global variable: {{ site }}.
	templates.AddVar("site", "example.com")

	return templates
}
```

```jet
{* ./views/page.jet *}
{{ extends "layouts/main.jet" }}

{{ block main() }}
<h1>{{ upper(.Title) }} on {{ site }}</h1>
{{ include "partials/footer.jet" }}
{{ end }}
```

- The layout argument is ignored. A template names its parent with `{{ extends }}`.
- jet reads a template from the file system when it is first rendered and keeps it. `Load()` parses every file to report the ones that do not parse, and empties that cache.
- `Reload(true)` parses all templates on every render, as in the other engines, and turns on jet's development mode, in which the template that is rendered is read again. Call it before the engine loads.
- jet runs `{{ include }}` when a template executes, which allows a template to include itself for every child of a tree. A template that does so without an end overflows the stack inside jet and takes the process with it. The engine cannot stop that one.
- `views.AddJetRuntimeVars(ctx, vars)` sets variables for one request. The entries of the request's data map are variables too, so `{{ title }}` and `{{ .title }}` both read a `title` that `view.KeyValue` set.
- `SetLoader` replaces the file system with a jet loader of your own.

### Pug

`views.Pug(dirOrFS, ".pug")` turns a Pug template into `html/template` text with jade and hands it to the HTML engine. It returns a `*views.HTMLEngine`, so layouts, the actions of the table above and the functions all work as they do there.

```pug
//- ./views/layouts/main.pug
doctype html
html
  body
    include ../partials/header.pug
    | {{ yield . }}
```

- `include` and `extends` read from the template directory of the engine. Write the file extension: `include partials/header.pug`. The path is relative to the file that includes.
- Data and functions are written in the `html/template` syntax: `p Hello {{ .Name }}`.

### Ace

`views.Ace(dirOrFS, ".ace")` compiles each Ace file into `html/template` text and hands it to the HTML engine. It returns a `*views.AceEngine`, which is an HTML engine with one more method, so layouts, the actions of the table above and the functions all work as they do there.

```ace
/ ./views/layouts/main.ace
= doctype html
html
  body
    = include partials/header.ace .
    {{ yield . }}
```

- `= include` becomes a `{{ template }}` action. Give it the name of the file with its extension, relative to the template directory.
- The base and inner templates of Ace, `= yield` and `= content`, are not available. Use a layout.
- The output is minified. `SetIndent("  ")` indents it.

## Other template engines

An engine that compiles its templates to Go code needs nothing from this package, because the Iris `Context` is an `io.Writer`. The examples for [hero](_examples/herotemplate), [quicktemplate](_examples/quicktemplate) and [templ](_examples/templ) show how.

Any type with this method is an engine for `view.Handler`:

```go
type Engine interface {
	ExecuteTemplate(w io.Writer, templateName string, layoutName string, data any) error
}
```

## Examples

| Example | Shows |
|:--------|:------|
| [overview](_examples/overview) | The HTML engine from a directory: maps, structs and functions |
| [embedding-templates-into-app](_examples/embedding-templates-into-app) | `embed.FS`, `Load` at startup, layouts per engine, per group and per render |
| [write-to](_examples/write-to) | Rendering into an `io.Writer` outside a request, for an e-mail body |
| [herotemplate](_examples/herotemplate) | hero, compiled templates |
| [quicktemplate](_examples/quicktemplate) | quicktemplate, compiled templates, with a test |
| [templ](_examples/templ) | templ components |

The last three are modules of their own, so that their template libraries are not dependencies of this package.

## Coming from Iris v12

The engines kept their names and their methods. Three things changed around them:

- An engine is registered with `view.Handler` and rendered with `view.Render`. The application and the context no longer have view methods of their own. [MIGRATION.md](https://github.com/kataras/iris/blob/main/MIGRATION.md) in the framework shows both sides.
- `ExecuteWriter` is now `ExecuteTemplate`.
- The framework no longer calls `Load` for you, and it does not have to: see [Loading](#loading).

## License

MIT, see [LICENSE](LICENSE).
