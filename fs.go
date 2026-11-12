package views

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kataras/iris/v14/context"
)

// templateFile is one template read from the file system of an engine.
type templateFile struct {
	name     string // slash separated and relative to the root of the file system.
	contents []byte
}

// readTemplates returns every file of fsys whose name ends with ext, in
// lexical order. The file system an engine gets for a nil or empty directory
// has no files.
func readTemplates(fsys fs.FS, ext string) ([]templateFile, error) {
	if isNoOpFS(fsys) {
		return nil, nil
	}

	var files []templateFile

	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() || (ext != "" && !strings.HasSuffix(name, ext)) {
			return nil
		}

		contents, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}

		files = append(files, templateFile{name: name, contents: contents})
		return nil
	})
	if err != nil {
		if dir, ok := fsys.(dirFS); ok {
			// A wrong directory is the usual mistake, and the error of
			// os.DirFS does not name it.
			return nil, fmt.Errorf("views: read templates from %s: %w", dir.name, err)
		}

		return nil, fmt.Errorf("views: read templates: %w", err)
	}

	return files, nil
}

// asset reads one file of fsys. The name may start with a slash.
func asset(fsys fs.FS, name string) ([]byte, error) {
	data, err := fs.ReadFile(fsys, strings.TrimPrefix(name, "/"))
	if err != nil {
		return nil, fmt.Errorf("asset: read file: %w", err)
	}

	return data, nil
}

func getFS(fsOrDir any) fs.FS {
	fsys := context.ResolveFS(fsOrDir)

	if name, ok := fsOrDir.(string); ok && !isNoOpFS(fsys) {
		return dirFS{FS: fsys, name: name}
	}

	return fsys
}

// dirFS is the file system of a directory that was given by its name. It
// keeps the name for the error of a load.
type dirFS struct {
	fs.FS
	name string
}

func isNoOpFS(fsys fs.FS) bool {
	return fsys == nil || context.IsNoOpFS(fsys)
}

// subFS returns the part of fsys below root, which is how RootDir points an
// engine at a directory inside an embed.FS. A root that names the whole file
// system ("", "." or "/") returns fsys itself.
//
// It panics when root is not a path inside fsys, such as "../views".
func subFS(fsys fs.FS, root string) fs.FS {
	root = strings.Trim(path.Clean(filepath.ToSlash(root)), "/")
	if isNoOpFS(fsys) || root == "" || root == "." {
		return fsys
	}

	sub, err := fs.Sub(fsys, root)
	if err != nil {
		panic(fmt.Errorf("views: RootDir: %w", err))
	}

	return sub
}

// resolveName returns the name a template was loaded under. A caller may leave
// the engine's extension out and may start the name with a slash, which is how
// Iris resolved names before version 14 moved that step into the engines.
// When nothing matches it returns the name as it was given.
func resolveName(name, ext string, exists func(string) bool) (string, bool) {
	if name == "" {
		return name, false
	}

	if exists(name) {
		return name, true
	}

	trimmed := strings.TrimPrefix(name, "/")
	if trimmed != name && exists(trimmed) {
		return trimmed, true
	}

	if ext != "" && !strings.HasSuffix(trimmed, ext) && exists(trimmed+ext) {
		return trimmed + ext, true
	}

	return name, false
}

// maxTemplateReads is how many other templates one template may pull in
// while it is parsed, through "include" and "extends". A template that
// includes or extends itself would otherwise recurse inside the parser until
// the stack or the memory runs out, and Go cannot recover from either.
const maxTemplateReads = 1000

// errTooManyReads is what a loader returns past maxTemplateReads.
var errTooManyReads = errors.New("views: too many templates read for one, does a template include or extend itself?")

// limitedFS is a file system that opens maxTemplateReads files and no more.
// A parser gets a new one for every template it parses.
type limitedFS struct {
	fs.FS
	reads int
}

func (l *limitedFS) Open(name string) (fs.File, error) {
	if l.reads >= maxTemplateReads {
		return nil, errTooManyReads
	}

	l.reads++
	return l.FS.Open(name)
}

func (l *limitedFS) exceeded() bool {
	return l.reads >= maxTemplateReads
}

// upsert returns a copy of list in which v takes the place of the element
// that same reports, or comes last when there is none.
func upsert[T any](list []T, v T, same func(T) bool) []T {
	list = slices.Clone(list)

	if i := slices.IndexFunc(list, same); i >= 0 {
		list[i] = v
		return list
	}

	return append(list, v)
}
