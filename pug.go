package views

import (
	"html/template"
	"net/http"

	"github.com/Joker/jade"
)

// Pug (or Jade) returns a new pug view engine.
// It shares the same exactly logic with the
// html view engine, it uses the same exactly configuration.
// The given "extension" MUST begin with a dot.
//
// A template is turned into html/template text first, so the actions of
// the html engine ({{ yield . }}, {{ render "name" . }} and the rest) and
// its layouts work the same. The "include" and "extends" keywords of Pug
// read from the engine's own file system. Give them the file extension, as
// in "include partials/footer.pug".
//
// Read more about the Jade Go Parser: https://github.com/Joker/jade
//
// Usage:
// Pug("./views", ".pug") or
// Pug(embed.FS, ".pug").RootDir("views") for embedded templates.
func Pug(fs any, extension string) *HTMLEngine {
	s := HTML(fs, extension)
	s.name = "Pug"
	s.middleware = func(name string, text []byte, _ template.FuncMap) (string, error) {
		// The parser gets the file system for this one parse. Its
		// package-level ReadFunc would be shared by every Pug engine of
		// the process. The file system counts what is read through it: a
		// template that includes or extends itself never ends otherwise.
		fsys := &limitedFS{FS: s.fs}

		contents, err := jade.ParseWithFileSystem(name, text, http.FS(fsys))
		if fsys.exceeded() {
			// The error of the parser is as deep as the recursion was.
			return "", errTooManyReads
		}

		return contents, err
	}
	return s
}
