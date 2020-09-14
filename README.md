![grpc logo](https://raw.githubusercontent.com/cncf/artwork/master/projects/grpc/horizontal/color/grpc-horizontal-color.png)
---
[![GoDev](https://img.shields.io/static/v1?label=godev&message=reference&color=00add8)](https://pkg.go.dev/github.com/johnsiilver/grpc/server)

These packages provide convienence wrappers around the Golang GRPC and GRPC Gateway projects.

There are two main packages:
- gateway/client - provides a REST client to GRPC when using grpc-gateway
- server - provides a GRPC runner that can mux GRPC/REST/HTTP on the same port and other helpful tidbits

## Why I Did This

I am developing a package that allows me to write pure Go + CSS WASM applications without resorting to JS, templates, HTML or syscall/js calls.

For that project, I wanted to write to GRPC from WASM, but the GRPC client won't run in WASM. This whole Envoy provy for that purpose(https://github.com/grpc/grpc-web) only makes sense to me when you have a team maintaing proxies for a company. The simplest solution is the GRPC Gateway REST endpoint.  

However, if you use REST and serve WASM on different ports, you now have to deal with CORS. I don't want to deal with CORS (simplicity makes for reliable services and is surprisingly hard to do).

So this solves those problems and provides some sane defaults around things like having at least gzip compression and easy ways to do http.Handler wrapping for middleware.

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

Usage Example:

```go
u, err := url.Parse("http://208.244.233.1:8080")
if err != nil {
    // Do something
}

resty := New(u)

resp := &pb.YourResp{}

err = resty.Call(
    context.Background(),
    "/v1/snippetsService/save", // This is the URL of the REST call, defined in your proto file
    &pb.YourReq{...}, // The protocol buffer you are sending
    resp, // The protocol buffer message you should receive
)
if err != nil {
    // Do something
}
```
The above will automatically gzip compress the REST request and sets the correct headers to work with our server package. If using against a standard GRPC Gateway instance, see notes in the godoc.

Note: This uses the new [Go protocol buffer library](https://blog.golang.org/protobuf-apiv2). I'm
not sure if this will work with protos generated by the older compiler. You may need a compiler that is in the > 1.4.x family from [here](https://github.com/golang/protobuf/). Instructions for installation are [here](https://grpc.io/docs/languages/go/quickstart/).

### server

Provides a wrapper that:
- Routing of GRPC/REST/HTTP traffic on a single port
- Applying TLS for all services, if you want it
- Allows using GRPC and other HTTP services on non-TLS connections using H2C
- REST Gateway to GRPC setup
- Applying common HTTP handler wrappers to the Gateway and HTTP handlers
- Applying gzip compression of HTTP/REST responses by default if client supports it, unless otherwise configured
- Applying gzip compression of GRPC responses by default, unless otherwise configured
- STAYING OUT OF THE WAY to allow you to customize your GRPC server, REST Gateway and HTTP

Usage Example:

```go
// Basic GRPC server setup.
grpcServer := grpc.NewSever()
myServer := myService.NewServer()
pb.RegisterMyServiceService(grpcServer, myServer) // Must happen prior to New() call.

// GRPC gateway setup, if doing a REST fronend. If you haven't done this before, you
// will need to follow the protocol buffer compiler setup, which is beyond the scope of this.
// https://grpc-ecosystem.github.io/grpc-gateway/docs/usage.html
gwMux := runtime.NewServeMux()

// Setup an HTTP Server serving all other pages.
httpMux := http.NewServeMux()
httpMux.Handle(
    "/",
    http.HandlerFunc(
        func(w http.ResponseWriter, r *http.Request) {
            w.Write([]byte("Hello World!"))
        },
    ),
)

// Example of basic HTTP/REST middleware.
logCallType := func(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request){
        switch r.Header.Get("content-type") {
        case "application/grpc-gateway":
            log.Println("request serviced by REST")
        default:
            log.Println("request is a normal HTTP request")
        }
        next.ServeHTTP(w, r) // Call the wrapped http.Handler
    })
}

// Creates our new wrapped service instances.
united, err := New(
    "208.244.233.1:8080",
    grpcServer,
    // This runs without TLS. Do not do this in production!!!!!!!!
    WithInsecure(),
    // This instantiates a GRPC REST Gateway on the same port for REST clients.
    Gateway(
        gwMux,
        pb.RegisterMyServerHandlerFromEndpoint,
        nil,
        []grpc.DialOption{grpc.WithInsecure()},
    ),
    // This serves an HTTP server handling everything that isn't GRPC REST on the same port.
    HTTP(httpMux, nil),
    // This wraps REST and HTTP calls with logCallType(). This does not wrap GRPC calls,
    // those must be done prior to New(). See the note on WrapHandlers().
    WrapHandlers(logCallType),
)
if err != nil {
    // Do something
}

// This provides a custom *http.Server. This is not required.
united.SetHTTPServer(
    &http.Server{
        ReadTimeout:    10 * time.Second,
        WriteTimeout:   10 * time.Second,
    },
)

// This starts all the wrapped servers. This will return, so if you do this in main() you
// will need a select{} or something to prevent main() from returning.
if err := united.Start(); err != nil {
    // Do something
}
```
The above will provide gzip or deflate HTTP/REST responses by default if the client supports it.

It will also gzip compress responses in GRPC, if the client sends with gzip compression enabled.

## Potential FAQs

### Can I contribute?

Yes, with caveats. Unless it is a bug fix, please send an email on what you propose before writing code. The goal here is simplicity and some use cases are better handled with by just forking the code for your own purposes.

### The release is v0.x.x, can we use it?

While we are still doing v0.x releases, it is possible that I will break the API. So you should tag to specific releases and not rely on the master branch.

### Why Gzip defaults instead of X

While I'm not sure which compression tech X will be, the answer would be the same. gzip is just universally supported.

I'm a fan of brotil myself, but:

- brotil requires HTTPS
- Many things don't support it
- The offical Go version requires CGO, which means NAY to being in the package by default!!!!
- Yeah I know dsnet has a pure Go one, but its not production ready, yet (looking forward to it dsnet)

Package includes options for adding other compression methods.

### In the server you provide default gzip/deflate compressors/decompressors, can I override them?

Yes, if you provide a faster compress/decompressor for encoding types "gzip" or "deflate" it will override the ones provided.

### You haven't updated this project in a while, still safe?

Unless I get bug requests, feature requests or I need something, I leave good code alone.

I try to respond to issues within a reasonable amount of time (a week), but remember that this is free code and I already have a job, go on vacations (when there isn't COVID), etc...

