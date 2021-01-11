/*
Package server provides a convienence type for merging GRPC/REST/HTTP services on a single port.
This avoids issues like needing CORS for JS/WASM clients to talk REST to your service because it
runs on a different port. It handles issues such as:
	- Routing all three services on a single port
	- Applying TLS for all services
	- REST Gateway to GRPC setup
	- Can not use TLS while using HTTP and GRPC services (a nightmare to figure out why it doesn't work)
	- Sane HTTP/REST default compression if client supported(gzip)
	- GRPC gzip compression support (though this is based on the client request using gzip)
	- Applying common HTTP handler wrappers to the Gateway and HTTP handlers (or individually)
	- STAYING OUT OF THE WAY to allow you to customize your GRPC server, REST Gateway and HTTP.

This package is meant to be used internally in a common GRPC platform package for a company. That
parent package can automatically setup health handlers, have options for ACL processing,
automatically tie into monitoring, etc...

Important Notes:
	- Traffic going to REST MUST have Content-Type set to "application/grpc-gateway".
	- The gateway/client located in this Repo automatically sets this.
	- We do not compress content being served with file types (as discovered by file extensions) that are already compressed

Usage example:
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
		// This serves an HTTP server handling everything that isn't GRPC on the same port.
		// This assumes that httpMux variable exists somewhere above.
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

	// This starts all the servers. This will return, so if you do this in main() you
	// will need a select{} or something to prevent main() from returning.
	if err := united.Start(); err != nil {
		// Do something
	}
*/
package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/grpc-ecosystem/grpc-gateway/runtime"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"

	_ "google.golang.org/grpc/encoding/gzip"
)

// GRPC wraps a GRPC server and optionally a GRPC Gateway and HTTP server on a single port.
type GRPC struct {
	insecure bool

	endpoint string
	lis      net.Listener
	serv     *grpc.Server
	certs    []tls.Certificate

	gateway    *runtime.ServeMux
	gwRegFn    GWRegistrationFunc
	gwWrappers []HTTPWrapper
	gwDialOpts []grpc.DialOption

	httpMux      *http.ServeMux
	httpWrappers []HTTPWrapper

	allHTTPWrappers []HTTPWrapper

	httpHandler    http.Handler
	gatewayHandler http.Handler

	httpServer  *http.Server
	http2Server *http2.Server

	cancelFuncs []context.CancelFunc

	// This section is used to deal with REST/HTTP compression/decompression.
	restDecompressors map[string]Decompressor
	httpCompressOrder []string
	httpCompressors   map[string]Compressor

	// mu locks our public methods.
	mu sync.Mutex
}

// Option provides an optional argument for the GRPC constructor.
type Option func(g *GRPC)

// WithTLS secures all services (GRPC/REST/HTTP) with the TLS certificates passed.
// If this option is not used, WithInsecure must be used.
func WithTLS(certs []tls.Certificate) Option {
	return func(g *GRPC) {
		g.certs = certs
	}
}

// WithInsecure is required in order to run without a TLS certificate.
func WithInsecure() Option {
	return func(g *GRPC) {
		log.Println("RUNNING WITHOUT ANY TRANSPORT SECURITY!!! TRAFFIC MAY BE INTERCEPTED BY THIRD PARTIES")
		g.insecure = true
	}
}

// GWRegistrationFunc is a function in your <service>.pb.gw.go file that is used to register a
// GRPC REST gateway to talk to the GRPC service. It is usually named Register<service>HandlerFromEndpoint().
type GWRegistrationFunc func(ctx context.Context, mux *runtime.ServeMux, endpoint string, opts []grpc.DialOption) error

// Gateway provides a GRPC REST gateway proxy that will answer all calls that have
// the header Content-Type set to "application/grpc-gateway". fn is a functions that registers
// this gateway with the GRPC server (see comments on GWRegistrationFunc type).  wrappers are
// http.Handler(s) that you want to wrap the gateway (like authoriztion or acl handlers). If
// you want to wrap both the gateway and an HTTP server in the same handlers instead use the
// WrapHandlers() option. Note: handlers that do response compression or decompression should
// be added with the HTTPCompress() and HTTPDecompress() options. dialOptions provides
// options that are needed to dial your GRPC service from the gateway.  You may need to provide
// dialOptions like grpc.WithInsecure() if you are using the WithInsecure() option above.
func Gateway(mux *runtime.ServeMux, fn GWRegistrationFunc, wrappers []HTTPWrapper, dialOptions []grpc.DialOption) Option {
	return func(g *GRPC) {
		if mux == nil {
			panic("cannot use Gateway() option with nil mux arguement")
		}
		if fn == nil {
			panic("cannot use Gateway() option with nil fn arguement")
		}
		g.gateway = mux
		g.gwRegFn = fn
		g.gwWrappers = wrappers
		g.gwDialOpts = dialOptions
	}
}

// HTTPWrapper provides an http.Hanlder that wraps another http.Handler. It is the responsibility
// of the HTTPWrapper implementation to call the wrapped Handler (or not to). A good reference on
// this pattern can be found at https://medium.com/@matryer/the-http-handler-wrapper-technique-in-golang-updated-bc7fbcffa702 .
type HTTPWrapper func(next http.Handler) http.Handler

// HTTP installs an http.ServeMux to handle all calls that do not have "application/grpc-gateway"
// or "application/grpc". wrappers provides http.Handler(s) that you want to wrap the mux in.
// If you want to install wrapper handlers on both the REST gateway and this ServeMux, you should
// use WrapHandlers(). Note: handlers that do response compression should be added with
// the HTTPCompress() option.
func HTTP(mux *http.ServeMux, wrappers []HTTPWrapper) Option {
	return func(g *GRPC) {
		g.httpMux = mux
		g.httpWrappers = wrappers
	}
}

// WrapHandlers wraps both the muxer passed with Gateway() and the the muxer passed to
// HTTP() with the handlers passed here. Note: handlers that do response compression should be
// added with the HTTPCompress() option.
// To wrap GRPC itself, you must use an UnaryServerInterceptor (defined at https://godoc.org/google.golang.org/grpc#UnaryServerInterceptor).
// You wrap it on the server using the UnaryInterceptor() ServerOption (https://godoc.org/google.golang.org/grpc#UnaryInterceptor)
// when doing grpc.NewServer().
func WrapHandlers(handlers ...HTTPWrapper) Option {
	return func(g *GRPC) {
		g.allHTTPWrappers = handlers
	}
}

// ResponseWriter is a composition of an io.WriteCloser and http.ResponseWriter. This is
// used when trying to define a new response compression. See examples in the compress.go file.
type ResponseWriter interface {
	io.WriteCloser
	http.ResponseWriter
}

// Compressor provides a function that composes an io.WriteCloser representing a compressor
// and the http.ResponseWriter into our local ResponseWriter that implements http.ResponseWriter
// with an additional Close() method for closing the compressor writes.
type Compressor func(w http.ResponseWriter) ResponseWriter

// Decompressor takes an io.Reader and either compresses the content or
// decompresses the content to the returned io.ReadCloser.
type Decompressor func(r io.Reader) (io.Reader, error)

// HTTPDecompress decompresses requests coming from the clients for REST matching on the client
// request's Encoding-Type. This puts a http.Handler before all other handlers provided to do the
// decompression. "gzip" and "deflate" are automatically provided.
func HTTPDecompress(encodingType string, decompressor Decompressor) Option {
	return func(g *GRPC) {
		g.restDecompressors[strings.ToLower(encodingType)] = decompressor
	}
}

// HTTPCompress compresses responses to HTTP clients if the client sent an Accept-Encoding for the
// type. Order of preference for the compress method will be: 1) The same as encoding on the request
// if provided, 2) The order they were added with this option.
func HTTPCompress(encodingType string, compressor Compressor) Option {
	return func(g *GRPC) {
		g.httpCompressors[strings.ToLower(encodingType)] = compressor
		g.httpCompressOrder = append(g.httpCompressOrder, strings.ToLower(encodingType))
	}
}

const (
	gzipEnc    = "gzip"
	deflateEnc = "deflate"
)

// New is the constructor for GRPC. pb.Register<Name>Service() must have been called on the serv argument.
func New(address string, serv *grpc.Server, options ...Option) (*GRPC, error) {
	g := &GRPC{
		endpoint:          address,
		serv:              serv,
		restDecompressors: map[string]Decompressor{},
		httpCompressors:   map[string]Compressor{},
	}

	for _, option := range options {
		option(g)
	}

	// If the user didn't provide a gzip encoder, we will provide ours.
	if _, ok := g.httpCompressors[gzipEnc]; !ok {
		g.httpCompressors[gzipEnc] = newGzipResponseWriter
		g.httpCompressOrder = append(g.httpCompressOrder, gzipEnc)
	}
	// If the user didn't provide a deflate encoder, we will provide ours.
	if _, ok := g.httpCompressors[deflateEnc]; ok {
		g.httpCompressors[deflateEnc] = newDeflateResponseWriter
		g.httpCompressOrder = append(g.httpCompressOrder, deflateEnc)
	}
	// If the user didn't provide a gzip decoder, we will provide ours.
	if _, ok := g.restDecompressors[gzipEnc]; !ok {
		g.restDecompressors[gzipEnc] = gzipDecompress
	}
	// If the user didn't provide a defalte decoder, we will provide ours.
	if _, ok := g.restDecompressors[deflateEnc]; !ok {
		g.restDecompressors[deflateEnc] = defalteDecompress
	}

	if len(g.certs) > 0 && g.insecure {
		return nil, fmt.Errorf("cannot use WithTLS() option and WithInsecure() option")
	}
	if !g.insecure && len(g.certs) == 0 {
		return nil, fmt.Errorf("if not using the WithTLS() option, must use WithInsecure()")
	}

	g.wrapGateway()
	g.wrapHTTP()

	return g, nil
}

// wrapGateway wraps the gateway in all the http.Handler(s) required.
func (g *GRPC) wrapGateway() {
	if g.gateway == nil {
		return
	}

	var handler http.Handler = g.gateway
	for _, wrap := range g.gwWrappers {
		handler = wrap(handler)
	}
	for _, wrap := range g.allHTTPWrappers {
		handler = wrap(handler)
	}
	g.gatewayHandler = handler
	return
}

// wrapHTTP wraps the http.Mux in all the http.Handler(s) required.
func (g *GRPC) wrapHTTP() {
	if g.httpMux == nil {
		return
	}

	var handler http.Handler = g.httpMux
	for _, wrap := range g.httpWrappers {
		handler = wrap(handler)
	}
	for _, wrap := range g.allHTTPWrappers {
		handler = wrap(handler)
	}
	g.httpHandler = handler
	return
}

// setupGateway sets the REST gateway up to make calls to GRPC. The net.Listener must already be
// started for this to be called.
func (g *GRPC) setupGateway() {
	if g.lis == nil {
		panic("bug: tried to do setupGateway before net.Listener was started")
	}

	if g.gatewayHandler != nil {
		grpcAddr := g.endpoint
		_, _, err := net.SplitHostPort(g.endpoint)
		if err != nil {
			grpcAddr = fmt.Sprintf("%s:%d", g.endpoint, g.Port())
		}
		ctx, cancel := context.WithCancel(context.Background())
		if err := g.gwRegFn(ctx, g.gateway, grpcAddr, g.gwDialOpts); err != nil {
			panic(err)
		}
		g.cancelFuncs = append(g.cancelFuncs, cancel)
	}
}

// SetHTTPServer allows you to provide a custom *http.Server. The fields
// Addr, Handler and TLSConfig in the http.Server will be overridden no matter what is provided here.
// If not provided, a default one is provided with just Handler and TLSConfig configured.
// This does not work after Start() has been called.
func (g *GRPC) SetHTTPServer(serv *http.Server) {
	g.httpServer = serv
}

// Start starts the server and enables listening for requests.
func (g *GRPC) Start() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	log.Println("starting GRPC service")

	if g.certs == nil {
		return g.startH2C()
	}
	return g.startTLS()
}

// ListeningOn is the address this GRPC server is currently listening on.
// Will return nil if it is not serving.
func (g *GRPC) ListeningOn() net.Addr {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.lis == nil {
		return nil
	}
	return g.lis.Addr()
}

func (g *GRPC) startTLS() error {
	lis, err := net.Listen("tcp", g.endpoint)
	if err != nil {
		return fmt.Errorf("error occurred while listening on tcp port: %v", err)
	}
	g.lis = lis

	g.setupGateway()

	var s = g.httpServer
	if s == nil {
		s = &http.Server{}
	}
	s.Addr = ""
	s.TLSConfig = &tls.Config{Certificates: g.certs}
	s.Handler = g.handler()
	s.TLSConfig.BuildNameToCertificate()

	log.Printf("all services (TLS) running on port %v", g.Port())
	go func() {
		if err := s.ServeTLS(lis, "", ""); err != nil {
			log.Println(err)
		}
	}()
	return nil
}

func (g *GRPC) startH2C() error {
	lis, err := net.Listen("tcp", g.endpoint)
	if err != nil {
		return fmt.Errorf("error occurred while listening on tcp port: %v", err)
	}
	g.lis = lis

	g.setupGateway()

	h2serv := g.http2Server
	if h2serv == nil {
		h2serv = &http2.Server{}
	}

	var s = g.httpServer
	if s == nil {
		s = &http.Server{}
	}
	s.Addr = ""
	s.TLSConfig = nil
	s.Handler = h2c.NewHandler(g.handler(), h2serv)

	log.Printf("GRPC service (no TLS) running on port %v", g.Port())
	go func() {
		if err := s.Serve(lis); err != nil {
			log.Println(err)
		}
	}()
	return nil
}

// handler handles routing between GRPC, REST and HTTP services.
func (g *GRPC) handler() http.Handler {
	return http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("Content-Type") {
			case "application/grpc":
				if r.ProtoMajor == 2 {
					g.serv.ServeHTTP(w, r)
					return
				}
				http.Error(w, "'application/grpc' content arrived, but HTTP protocol was not HTTP 2", http.StatusBadRequest)
				return
			case "application/grpc-gateway":
				if g.gatewayHandler == nil {
					http.Error(w, "application/grpc-gateway received, but server is not setup for REST", http.StatusBadRequest)
					return
				}
				g.httpRESTHandler(g.gatewayHandler).ServeHTTP(w, r)
				return
			default:
				// Special case where they are looking for the REST swagger docs.
				if strings.HasPrefix(r.URL.Path, "/swagger-ui/") {
					g.httpRESTHandler(g.gatewayHandler).ServeHTTP(w, r)
					return
				}

				if g.httpMux == nil {
					http.Error(w, "Not Found", http.StatusNotFound)
					return
				}
				g.httpRESTHandler(g.httpMux).ServeHTTP(w, r)
			}
		},
	)
}

// compressDecompressHandler is our parent handler that calls all our compress/decompress
// handlers for HTTP and REST services.
func (g *GRPC) httpRESTHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			handler := g.decompressHandler(
				g.compressHandler(
					next,
				),
			)
			handler.ServeHTTP(w, r)
		},
	)
}

// compressHandler is a handler that compresses responses to the caller. This will attempt to
// use the same compression method that the caller used on their request, failing that it will
// check each compression method we support against the ones the client accepts until the first
// match. This should wrap all HTTP and REST calls (but NOT GRPC).
func (g *GRPC) compressHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			// Certainly types of return content, indicated by file extentions, should not get gzip compression as
			// they are already compressed.
			if doNotCompress[strings.ToLower(path.Ext(r.URL.Path))] {
				next.ServeHTTP(w, r)
				return
			}
			if contentEncoding := r.Header.Get("Content-Encoding"); contentEncoding != "" {
				if compressorFn, ok := g.httpCompressors[contentEncoding]; ok {
					w.Header().Add("Content-Encoding", contentEncoding)
					rw := compressorFn(w)
					defer rw.Close()
					next.ServeHTTP(rw, r)
					return
				}
			}
			for _, acceptEncoding := range r.Header.Values("Accept-Encoding") {
				if compressorFn, ok := g.httpCompressors[acceptEncoding]; ok {
					w.Header().Add("Content-Encoding", acceptEncoding)
					rw := compressorFn(w)
					defer rw.Close()
					next.ServeHTTP(rw, r)
					return
				}
			}
		},
	)
}

// decompressHandler tries to decompress
func (g *GRPC) decompressHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if contentEncoding := r.Header.Get("Content-Encoding"); contentEncoding != "" {
				decompressorFn, ok := g.restDecompressors[contentEncoding]
				if !ok {
					http.Error(w, fmt.Sprintf("request content-encoding=%s, server does not support this", contentEncoding), http.StatusPreconditionFailed)
					return
				}
				reader, err := decompressorFn(r.Body)
				if err != nil {
					http.Error(w, fmt.Sprintf("problem with decompressor(%s): %s", contentEncoding, err), http.StatusBadRequest)
					return
				}
				r.Body = ioutil.NopCloser(reader)
				next.ServeHTTP(w, r)
			}
		},
	)
}

// Stop stops the server gracefully and stops accepting new requests.
func (g *GRPC) Stop() {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g == nil {
		return
	}

	for _, cancel := range g.cancelFuncs {
		cancel()
	}

	g.serv.GracefulStop()

	g.lis = nil
}

// Port returns the port the server is running on.
func (g *GRPC) Port() int {
	return g.lis.Addr().(*net.TCPAddr).Port
}
