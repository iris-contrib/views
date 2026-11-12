// Package views holds the template engines of the Iris web framework: HTML,
// Django, Handlebars, Jet, Pug and Ace.
//
// Every engine satisfies the three interfaces of
// github.com/kataras/iris/v14/middleware/view (Engine, EngineFuncer and
// EngineExtenser), so it is registered with view.Handler and rendered with
// view.Render:
//
//	templates := views.HTML("./views", ".html")
//	app.Use(view.Handler(templates))
//
//	app.Get("/", func(ctx iris.Context) {
//		view.Render(ctx, "index.html", "layouts/main.html", iris.Map{"Title": "Home"})
//	})
//
// # Loading
//
// The six engines load their templates the same way:
//
//   - Load parses every template and returns the first error. Call it at
//     startup to refuse to start with a template that does not parse.
//   - An engine that was never loaded loads itself on its first
//     ExecuteTemplate. Requests that arrive together wait for that one load.
//     If it fails, every render returns its error until a Load succeeds.
//   - Reload(true) parses the templates again on every render, for
//     development.
//
// A template or a layout that does not exist is a view.ErrTemplateNotFound.
// A name may leave the engine's extension out and may start with a slash.
package views

import (
	"errors"

	"github.com/kataras/iris/v14/middleware/view"
)

type (
	// Engine is the interface for a compatible Iris view engine.
	// It's an alias of view.Engine.
	Engine = view.Engine
)

// ErrTemplateNotFound reports whether a template was not found in the parsed templates tree.
type ErrTemplateNotFound = view.ErrTemplateNotFound

// ErrNoTemplates is returned by Load, and by the first render of an engine
// that was never loaded, when there is nothing to load: no file with the
// engine's extension and no template added with ParseTemplate. The usual
// causes are a wrong directory and a wrong extension.
var ErrNoTemplates = errors.New("views: no templates found")

// NoLayout disables the configuration's layout for a specific execution.
const NoLayout = "iris.nolayout"

// returns empty if it's no layout or empty layout and empty configuration's layout.
func getLayout(layout string, globalLayout string) string {
	if layout != "" {
		if layout == NoLayout {
			return ""
		}

		return layout
	}

	return globalLayout
}
