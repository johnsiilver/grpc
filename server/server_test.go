package server

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	restClient "github.com/johnsiilver/grpc/gateway/client"
	server "github.com/johnsiilver/grpc/gateway/client/testdata/grpc"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/grpc-ecosystem/grpc-gateway/runtime"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/testing/protocmp"

	pb "github.com/johnsiilver/grpc/gateway/client/testdata/grpc/proto"
)

func TestBasics(t *testing.T) {
	endpoint := "127.0.0.1:8080"

	dataPath := filepath.Join(os.TempDir(), uuid.New().String())

	if err := os.MkdirAll(dataPath, 0700); err != nil {
		panic(err)
	}

	// Setup our GRPC server.
	grpcServer := grpc.NewServer()
	grpcImpl := server.NewService(dataPath)
	pb.RegisterSnippetsServer(grpcServer, grpcImpl)

	// Setup the REST Gateway.
	gwMux := runtime.NewServeMux()

	// Setup an HTTP Server.
	httpMux := http.NewServeMux()
	httpMux.Handle(
		"/",
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte("Hello World!"))
			},
		),
	)

	// Setup our united server.
	united, err := New(
		endpoint,
		grpcServer,
		// This runs without TLS. Do not do this in production!!!!!!!!
		WithInsecure(),
		// This instantiates a GRPC REST Gateway on the same port for REST clients.
		Gateway(
			gwMux,
			pb.RegisterSnippetsHandlerFromEndpoint,
			nil,
			[]grpc.DialOption{grpc.WithInsecure()},
		),
		// This serves an HTTP server handling everything that isn't GRPC on the same port.
		// This assumes that httpMux variable exists somewhere above.
		HTTP(httpMux, nil),
	)
	if err != nil {
		panic(err)
	}

	united.SetHTTPServer(
		&http.Server{
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
		},
	)

	if err := united.Start(); err != nil {
		panic(err)
	}
	defer united.Stop()

	// Test REST.
	unixNano := time.Now().UnixNano()
	u, err := url.Parse("http://" + endpoint)
	if err != nil {
		panic(err)
	}
	resty := restClient.New(u)

	err = resty.Call(
		context.Background(),
		"/v1/snippetsService/save",
		&pb.SaveReq{
			UnixNano: unixNano,
			Content:  "Hello",
		},
		&pb.SaveResp{},
	)
	if err != nil {
		t.Fatalf("TestBasics(REST test .Save): got err == %s, want err == nil", err)
	}

	resp := &pb.GetResp{}
	err = resty.Call(
		context.Background(),
		"/v1/snippetsService/get",
		&pb.GetReq{
			UnixNano: unixNano,
		},
		resp,
	)
	if err != nil {
		t.Fatalf("TestBasics(REST test .Get): got err == %s, want err == nil", err)
	}

	wantResp := &pb.GetResp{UnixNano: unixNano, Content: "Hello"}
	if diff := cmp.Diff(wantResp, resp, protocmp.Transform()); diff != "" {
		t.Errorf("TestBasics(REST test .Get): -want/+got:\n%s", diff)
	}

	// Test GRPC.
	conn, err := grpc.Dial(endpoint, grpc.WithInsecure())
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	grpcClient := pb.NewSnippetsClient(conn)
	gResp, err := grpcClient.Get(
		context.Background(),
		&pb.GetReq{UnixNano: unixNano},
	)
	if err != nil {
		t.Errorf("TestBasics(GRPC test .Get): got err == %s, want err == nil", err)
	}

	if diff := cmp.Diff(wantResp, gResp, protocmp.Transform()); diff != "" {
		t.Errorf("TestBasics(GRPC test .Get): -want/+got:\n%s", diff)
	}
}
