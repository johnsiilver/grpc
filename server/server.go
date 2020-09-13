/*
Package server provides a convienence type for merging GRPC/REST/HTTP services on a single port.
This avoids issues like needing CORS for JS/WASM clients to talk REST to your service because it
runs on a different port. It handles issues such as:
	- Routing for all three services on a single port
	- Applying TLS for all services
	- REST Gateway to GRPC setup
	- Using not using TLS and needing HTTP with GRPC (a nightmare to figure out why it doesn't work)
	- Applying common HTTP handler wrappers to the Gateway and HTTP handlers (or individually)
	- STAYING OUT OF THE WAY to allow you to customize your GRPC server, REST Gateway and HTTP.

This package is meant to be used internally in a common GRPC platform package for a company. That
parent package can automatically setup health handlers, have options for ACL processing,
automatically tie into monitoring, etc...

Important Note:
	- Traffic going to REST MUST have Content-Type set to "application/grpc-gateway".
	- The gateway/client located in this Repo automatically sets this.

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
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/grpc-ecosystem/grpc-gateway/runtime"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
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
// WrapHandlers() option. dialOptions provides options that are needed to dial your GRPC service
// from the gateway.  You may need to provide dialOptions like grpc.WithInsecure() if you are
// using the WithInsecure() option above.
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
// or "application/grpc" and are calling with HTTP2. wrappers provides http.Handler(s) that you
// want to wrap mux in. If you want to install wrapper handlers on
// both the REST gateway and this ServeMux, you should use WrapHandlers().
func HTTP(mux *http.ServeMux, wrappers []HTTPWrapper) Option {
	return func(g *GRPC) {
		g.httpMux = mux
		g.httpWrappers = wrappers
	}
}

// WrapHandlers wraps the Gateway and the the handler passed to HTTP with the handlers passed here.
// To wrap GRPC itself, you must use an UnaryServerInterceptor (defined at https://godoc.org/google.golang.org/grpc#UnaryServerInterceptor).
// You wrap it on the server using the UnaryInterceptor() ServerOption (https://godoc.org/google.golang.org/grpc#UnaryInterceptor)
// when doing grpc.NewServer().
func WrapHandlers(handlers ...HTTPWrapper) Option {
	return func(g *GRPC) {
		g.allHTTPWrappers = handlers
	}
}

// New is the constructor for GRPC. pb.Register<Name>Service() must have been called on the serv argument.
func New(address string, serv *grpc.Server, options ...Option) (*GRPC, error) {
	g := &GRPC{
		endpoint: address,
		serv:     serv,
	}

	for _, option := range options {
		option(g)
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
				g.gatewayHandler.ServeHTTP(w, r)
				return
			default:
				if g.httpMux == nil {
					http.Error(w, "Not Found", http.StatusNotFound)
					return
				}
				g.httpMux.ServeHTTP(w, r)
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
