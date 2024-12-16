package views

import (
	"github.com/kataras/iris/v14/middleware/view"
)

type (
	// Engine is the interface for a compatible Iris view engine.
	// It's an alias of view.Engine.
	Engine = view.Engine
)

// ErrTemplateNotFound reports whether a template was not found in the parsed templates tree.
type ErrTemplateNotFound = view.ErrTemplateNotFound

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
