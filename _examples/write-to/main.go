// An engine renders into any io.Writer, with no request and no server.
//
// ExecuteTemplate is the whole view.Engine interface, and it is all that
// view.Render calls. This program calls it itself to build the body of an
// e-mail: the page email/simple.html inside the layout layouts/email.html,
// written into a strings.Builder. The same call writes to a file, to a
// bytes.Buffer in a test or to os.Stdout.
//
// Learn: render a template outside a handler.
//
// Run:
//
//	go run .
//
// It prints the HTML of the e-mail.
//
// Next: ../herotemplate
package main

import (
	"embed"
	"fmt"
	"log"
	"strings"

	"github.com/iris-contrib/views"
)

// views/email/simple.html and views/layouts/email.html.
//
//go:embed views
var viewsFS embed.FS

type mailData struct {
	Title    string
	Body     string
	RefTitle string
	RefLink  string
}

func main() {
	templates := views.HTML(viewsFS, ".html").RootDir("views")
	if err := templates.Load(); err != nil {
		log.Fatal(err)
	}

	// The writer. Hand body.String() to the package you send e-mails with.
	var body strings.Builder

	err := templates.ExecuteTemplate(&body, "email/simple.html", "layouts/email.html", mailData{
		Title:    "This is my e-mail title",
		Body:     "This is my e-mail body",
		RefTitle: "Iris web framework",
		RefLink:  "https://iris-go.com",
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(body.String())
}
