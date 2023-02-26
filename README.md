# BSR remote packages debug tool

This is an experimental command that prints the [BSR Remote Package](https://docs.buf.build/bsr/remote-packages/overview) version based on plugin and module inputs.

This command uses the BSR Go module proxy to obtain a client and hit the BSR API endpoints to
resolve references.

```
require (
	buf.build/gen/go/bufbuild/buf/bufbuild/connect-go v1.5.2-20230222163843-c806e06ce889.1
	buf.build/gen/go/bufbuild/buf/protocolbuffers/go v1.28.1-20230222163843-c806e06ce889.4
)
```

Note the latest plugin and module versions resolve to today's available versions, but could change
tomorrow. Here's an example:

```sh
$ go run main.go bufbuild/connect-go:latest buf.build/bufbuild/buf:latest | jq -r .version

v1.5.2-20230222163843-c806e06ce889.1

$ go run main.go protocolbuffers/go:latest buf.build/bufbuild/buf:latest | jq -r .version

v1.28.1-20230222163843-c806e06ce889.4
```

The module reference can be `main` (which is same as `latest`), a tag, or a draft.

E.g., the [bufbuild/buf](https://buf.build/bufbuild/buf) module has a tag (`60ee4a1ad9fe22392fea98967d858de8f15131ac`) that
resolves to a specific commit (`c806e06ce8894eee9ba4932f95c8fa43`).

We use the tag as a reference:

```sh
$ go run main.go bufbuild/connect-es:latest buf.build/bufbuild/buf:60ee4a1ad9fe22392fea98967d858de8f15131ac | jq -r .version

v0.8.0-20230222163843-c806e06ce889.1
```

The same will work for [drafts](https://docs.buf.build/bsr/overview#referencing-a-module)!

### Bonus

This command also returns the `go get ...` or `npm install ...` command with helpful hints:

#### Go

```sh
$ go run main.go bufbuild/connect-go:latest buf.build/bufbuild/buf:main | jq -r .command

go get buf.build/gen/go/bufbuild/buf/bufbuild/connect-go@v1.5.2-20230222163843-c806e06ce889.1

$ go run main.go bufbuild/connect-go:latest buf.build/bufbuild/buf:main | jq -r .hint

Set GOPRIVATE=buf.build/gen/go

See https://docs.buf.build/bsr/remote-packages/go#private for more details.
```

#### NPM

```sh
$ go run main.go bufbuild/connect-es:latest buf.build/bufbuild/buf:main | jq -r .command

npm install @buf/bufbuild_buf.bufbuild_connect-es@v0.8.0-20230222163843-c806e06ce889.1

$ go run main.go bufbuild/connect-es:latest buf.build/bufbuild/buf:main | jq -r .hint

Don't forget to update the registry config:

        npm config set @buf:registry https://buf.build/gen/npm/v1/

For private modules you'll need to set the token:

        npm config set //buf.build/gen/npm/v1/:_authToken {token}

See https://docs.buf.build/bsr/remote-packages/npm#private for more details.
```
