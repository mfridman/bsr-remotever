# BSR Remote Packages debug tool

An experimental command to print a
[BSR Remote Package](https://docs.buf.build/bsr/remote-packages/overview) version based on plugin
and module inputs.

Bonus, the tool itself leverages Remote Packages (BSR Go module proxy) to download a
[connect-go client](https://connect.build/docs/go) for the
[Buf Schema Registry](https://docs.buf.build/bsr/introduction) API.

The API is backed by [connect](https://connect.build/), and the protobuf definitions are publicly
available at [bufbuild/buf](https://buf.build/bufbuild/buf).

> No protobuf, no compilers, no code generation. Just `go get ...` and you're up and running with an
> API client in seconds!

### Installation

```sh
go install github.com/mfridman/bsr-remotever@latest
```

### Example

The [go.mod](https://github.com/mfridman/bsr-remotever/blob/main/go.mod) in this repository depends
on 2 Go packages from the BSR Go module proxy:

```
require (
	buf.build/gen/go/bufbuild/buf/bufbuild/connect-go v1.5.2-20230222163843-c806e06ce889.1
	buf.build/gen/go/bufbuild/buf/protocolbuffers/go v1.28.1-20230222163843-c806e06ce889.4
)
```

You typically `go get ... @latest` without worrying about versions, so you always have an up-to-date
package, which is a combination of the latest plugin by semver and latest module by the latest
commit on `main`.

But, in the odd event we need to obtain the version based on some references, we can use this tool
like so:

- plugin: `bufbuild/connect-go:latest`
- module: `buf.build/bufbuild/buf:latest`

```sh
$ bsr-remotever bufbuild/connect-go:latest buf.build/bufbuild/buf:latest | jq -r .version

v1.5.2-20230222163843-c806e06ce889.1
```

- plugin: `protocolbuffers/go:latest`
- module: `buf.build/bufbuild/buf:latest`

```sh
$ bsr-remotever protocolbuffers/go:latest buf.build/bufbuild/buf:latest | jq -r .version

v1.28.1-20230222163843-c806e06ce889.4
```

The module reference can be `latest` (same as `main`), a tag, or a draft.

E.g., the [bufbuild/buf](https://buf.build/bufbuild/buf) module has a tag
(`60ee4a1ad9fe22392fea98967d858de8f15131ac`) that resolves to a specific commit
(`c806e06ce8894eee9ba4932f95c8fa43`).

We use the tag as a module reference:

```sh
$ bsr-remotever bufbuild/connect-es:latest buf.build/bufbuild/buf:60ee4a1ad9fe22392fea98967d858de8f15131ac | jq -r .version

v0.8.0-20230222163843-c806e06ce889.1
```

The same will work for [drafts](https://docs.buf.build/bsr/overview#referencing-a-module)!

### Bonus

This command also returns the `go get ...` or `npm install ...` commands depending on the plugin,
with helpful hints:

#### Go

```sh
$ bsr-remotever bufbuild/connect-go:latest buf.build/bufbuild/buf:main | jq -r .command

go get buf.build/gen/go/bufbuild/buf/bufbuild/connect-go@v1.5.2-20230222163843-c806e06ce889.1

$ bsr-remotever bufbuild/connect-go:latest buf.build/bufbuild/buf:main | jq -r .hint

Set GOPRIVATE=buf.build/gen/go

See https://docs.buf.build/bsr/remote-packages/go#private for more details.
```

#### NPM

```sh
$ bsr-remotever bufbuild/connect-es:latest buf.build/bufbuild/buf:main | jq -r .command

npm install @buf/bufbuild_buf.bufbuild_connect-es@v0.8.0-20230222163843-c806e06ce889.1

$ bsr-remotever bufbuild/connect-es:latest buf.build/bufbuild/buf:main | jq -r .hint

Don't forget to update the registry config:

        npm config set @buf:registry https://buf.build/gen/npm/v1/

For private modules you'll need to set the token:

        npm config set //buf.build/gen/npm/v1/:_authToken {token}

See https://docs.buf.build/bsr/remote-packages/npm#private for more details.
```
