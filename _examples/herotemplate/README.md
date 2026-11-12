# Hero template example

The Iris version of the example that [hero](https://github.com/shiyanhui/hero) ships: https://github.com/shiyanhui/hero/tree/master/examples/app.

hero compiles its templates to Go functions. A generated function writes into a `*bytes.Buffer` or into an `io.Writer`, and the Iris `Context` is an `io.Writer`, so a handler calls it directly. No engine of the `views` package is involved.

Against the original `net/http` example, a handler changes in two places:

```go
// net/http
if _, err := w.Write(buffer.Bytes()); err != nil {
// and
template.UserListToWriter(userList, w)
```

```go
// Iris
ctx.HTML(buffer.String())
// and
template.UserListToWriter(userList, ctx)
```

## Run

```sh
go run .
```

```sh
curl localhost:8080/users
curl localhost:8080/users/writer
```

The Go code under `template` is generated and committed. After a change to a template, generate it again:

```sh
go install github.com/shiyanhui/hero/hero@latest
hero -source=./template
```
