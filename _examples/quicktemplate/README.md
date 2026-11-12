# Quicktemplate example

[quicktemplate](https://github.com/valyala/quicktemplate) compiles its templates to Go code. The generated code writes into an `io.Writer`, and the Iris `Context` is an `io.Writer`, so a handler calls it directly. No engine of the `views` package is involved.

## Run

The generated `*.qtpl.go` files are committed, so the example runs as it is:

```sh
go run .
```

```sh
curl localhost:8080/
curl localhost:8080/yourname
```

The test sends the same two requests in memory:

```sh
go test .
```

## Changing a template

The templates are the `*.qtpl` files under `templates`. After a change, generate the Go code again with the quicktemplate compiler, `qtc`, and build the program again:

```sh
go install github.com/valyala/quicktemplate/qtc@latest
qtc -dir=./templates
```

The full documentation is at https://github.com/valyala/quicktemplate.
