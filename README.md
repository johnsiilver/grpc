![grpc logo](https://grpc.io/img/logos/grpc-logo.png)
---
[![GoDev](https://img.shields.io/static/v1?label=godev&message=reference&color=00add8)](https://pkg.go.dev/github.com/johnsiilver/grpc/server)

These packages provide convienence wrappers around the Golang GRPC and GRPC Gateway projects.

There are two main packages:
- gateway/client - provides a REST client to GRPC when using grpc-gateway
- server - provides a GRPC runner that can mux GRPC/REST/HTTP on the same port and other helpful tidbits

## Why I Did This

Short version: 

I hate developing web apps. Its by far the worst application framework ever cobbled together, like some frankenstein monster.

I wanted to use WASM, but that was only changing this nightmare from:

```Javascript+HTML+CSS+Go+Go Templates+Some Framework```

to

```Javascript+HTML+CSS+Go+Go Templates+WASM+Possibly Some Framework```

While there is more Go (an improvement), it didn't eliminate the others.

So I developed/developing a package that allows me to write pure Go + CSS, nothing else. And a goal is to never use syscall/js (syscall/js is my assembly code and I'm trying to build my C language for the web). 

I wanted to use GRPC from WASM, but the GRPC client won't run in WASM. So I need to use REST. If you use REST and serve WASM on different ports, you now have to deal with CORS. 

I don't want to deal with CORS.

So this fixes that and provides some sane defaults around HTTP/REST (like at least gzip compression). GRPC/net.HTTP/GRPC-Gateway out of the box don't provide sane defaults (I'm not saying
they should).

## Packages

### gateway/client

This simply makes a REST client (if you are using the GRPC Gateway) that supports:
- The service package by tagging the request header "Content-Type": "application/grpc-gateway"
- Automatic marshal/unmarshal from JSON into Protocol Buffers
- Requests gzip/deflate responses by default (if the REST server supports it)
- Can provides gzip/deflate request compression (if the REST server supports it) with a simple option
- Easily add your own compression/decompression
- Provide your own custom *http.Client as an option
- A simple calling mechanism

Note: This uses the new [Go protocol buffer library](https://blog.golang.org/protobuf-apiv2). I'm
not sure if this will work with protos generated by the older compiler. You may need a compiler that is in the > 1.4.x family from [here](https://github.com/golang/protobuf/). Instructions for installation are [here](https://grpc.io/docs/languages/go/quickstart/).

### server

Provides a wrapper that provides:
- Routing of GRPC/REST/HTTP traffic on a single port
- Applying TLS for all services, if you want it
- Allows using GRPC and other HTTP services on non-TLS connections using H2C (which is a pain to figure out you need)
- REST Gateway to GRPC setup
- Applying common HTTP handler wrappers to the Gateway and HTTP handlers
- Applying gzip compression of HTTP/REST responses by default if client supports it unless otherwise configured
- Applying gzip compression of GRPC responses by default unless otherwise configured
- STAYING OUT OF THE WAY to allow you to customize your GRPC server, REST Gateway and HTTP

### server/defaults

Provides some default options you should put on your server before using our server.New() call. Like the ability to decompress gzip/deflate.

## Potential FAQs

### Can I contribute?

The short answer is yes. Unless it is a bug fix, please send an email on what you propose. The goal is simplicity and some use cases are better handled with forks.

### The release is v0.1.x, can we use it?

While we are still doing v0.x releases, it is possible that I will break the API. So you should tag to specific releases and not rely on the master branch.

### Why Gzip defaults instead of X

While I'm not sure which compression tech X will be, the answer will always be the same. gzip is just universally supported.

I'm a fan of brotil myself, but:

- brotil requires HTTPS
- Many things don't support it
- The Go version requires CGO, which means NAY!!!!
- Yeah I know dsnet has one, but it ain't production ready

### In the server you provide default gzip/deflate compressors/decompressors, can I override them?

Yes, if you provide a faster compress/decompressor for encoding types "gzip" or "defalte" it will
override the ones provided.

### You haven't updated this project in a while, still safe?

Unless I get bug requests, feature requests or I need something, I leave good code alone.

I try to respond to issues within a reasonable amout of time (a week), but remember that this is free code and I already have a job, go on vacations (when there isn't COVID), etc...

