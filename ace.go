package views

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/kataras/iris/v14/middleware/view"

	"github.com/yosssi/ace"
)

// AceEngine represents the Ace view engine.
// See the `Ace` package-level function for more.
type AceEngine struct {
	*HTMLEngine

	indent string
}

var (
	_ view.Engine         = (*AceEngine)(nil)
	_ view.EngineFuncer   = (*AceEngine)(nil)
	_ view.EngineExtenser = (*AceEngine)(nil)
)

// SetIndent string used for indentation.
// Do NOT use tabs, only spaces characters.
// Defaults to minified response, no indentation.
func (s *AceEngine) SetIndent(indent string) *AceEngine {
	s.indent = indent
	return s
}

// Ace returns a new Ace view engine.
// It shares the same exactly logic with the
// html view engine, it uses the same exactly configuration.
// The given "extension" MUST begin with a dot.
// Ace minifies the response automatically unless
// SetIndent() method is set.
//
// Every file is compiled on its own into html/template text, so the
// actions of the html engine ({{ yield . }}, {{ render "name" . }} and the
// rest) and its layouts work the same. The "= include" helper of Ace
// becomes a {{ template }} action: give it the name of the file with its
// extension, as in "= include partials/footer.ace .". The base and inner
// templates of Ace ("= yield" and "= content") are not available, use a
// layout instead.
//
// Read more about the Ace Go Parser: https://github.com/yosssi/ace
//
// Usage:
// Ace("./views", ".ace") or
// Ace(embed.FS, ".ace").RootDir("views") for embedded templates.
func Ace(fs any, extension string) *AceEngine {
	s := &AceEngine{HTMLEngine: HTML(fs, extension), indent: ""}
	s.name = "Ace"

	s.middleware = func(name string, text []byte, funcs template.FuncMap) (contents string, err error) {
		src := ace.NewSource(
			ace.NewFile(name, text),
			ace.NewFile("", []byte{}),
			[]*ace.File{},
		)

		opts := &ace.Options{
			Extension:  strings.TrimPrefix(extension, "."),
			FuncMap:    funcs,
			DelimLeft:  s.left,
			DelimRight: s.right,
			Indent:     s.indent,
		}

		rslt, err := ace.ParseSource(src, opts)
		if err != nil {
			return "", err
		}

		t, err := ace.CompileResult(name, rslt, opts)
		if err != nil {
			return "", err
		}

		compiled := t.Lookup(name)
		if compiled == nil || compiled.Tree == nil {
			return "", fmt.Errorf("ace: nothing was compiled")
		}

		return compiled.Tree.Root.String(), nil
	}

	return s
}
