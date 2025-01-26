package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/grpcutil"
	"github.com/grafana/dskit/httpgrpc"
	"go.uber.org/atomic"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

var (
	httpCount   = atomic.NewInt64(0)
	unaryCount  = atomic.NewInt64(0)
	streamCount = atomic.NewInt64(0)
)

type helloWorldHTTPMiddleware struct {
	logger log.Logger
}

func (m helloWorldHTTPMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if httpCount.Inc()%1000 == 0 {
			level.Info(m.logger).Log("url", r.URL.String(), "method", r.Method, "msg", "Hello, World!!!", "req", requestToString(r))
		}
		next.ServeHTTP(w, r)
	})
}

func HelloWorldHTTPMiddleware(logger log.Logger) Interface {
	return helloWorldHTTPMiddleware{logger: logger}
}

func HelloWorldUnaryClientInterceptor(cluster string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if cluster != "" {
			ctx = grpcutil.AppendClusterToOutgoungContext(ctx, cluster)
		}

		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func HelloWorldUnaryServerInterceptor(logger log.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if unaryCount.Inc()%1000 == 0 {
			clusterID, ok := grpcutil.GetClusterFromIncomingContext(ctx)
			if !ok {
				clusterID = "<unknown>"
			}
			level.Info(logger).Log("server", info.Server, "method", info.FullMethod, "msg", "Hello, World!!!", "req", requestToString(req), "context", contextToString(ctx), "cluster", clusterID)
		}
		return handler(ctx, req)
	}
}

func contextToString(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "no metadata found in incoming context"
	}
	return fmt.Sprintf("%v", md)
}

func requestToString(req interface{}) string {
	httpGrpcReq, ok := req.(*httpgrpc.HTTPRequest)
	if ok {
		repeatedStringForHeaders := "[]*Header{"
		for _, f := range httpGrpcReq.Headers {
			repeatedStringForHeaders += strings.Replace(f.String(), "Header", "Header", 1) + ","
		}
		repeatedStringForHeaders += "}"

		return strings.Join([]string{`&httpgrpc.HTTPRequest{`,
			`Method:` + fmt.Sprintf("%v", httpGrpcReq.Method) + `,`,
			`Url:` + fmt.Sprintf("%v", httpGrpcReq.Url) + `,`,
			`Headers:` + repeatedStringForHeaders + `,`,
			`}`,
		}, "")
	}
	httpReqPointer, ok := req.(*http.Request)
	if ok {
		repeatedStringForHeaders := "[]*Header{"
		for key, values := range httpReqPointer.Header {
			s := strings.Join([]string{`&Header{`,
				`Key:` + fmt.Sprintf("%v", key) + `,`,
				`Values:` + fmt.Sprintf("%v", values) + `,`,
				`}`,
			}, "")
			repeatedStringForHeaders += s + ","
		}
		repeatedStringForHeaders += "}"
		s := strings.Join([]string{`&http.Request{`,
			`Method:` + fmt.Sprintf("%v", httpReqPointer.Method) + `,`,
			`Url:` + fmt.Sprintf("%v", httpReqPointer.URL) + `,`,
			`Headers:` + repeatedStringForHeaders + `,`,
			`}`,
		}, "")
		return s
	}
	httpReq, ok := req.(http.Request)
	if ok {
		repeatedStringForHeaders := "[]*Header{"
		for key, values := range httpReq.Header {
			s := strings.Join([]string{`&Header{`,
				`Key:` + fmt.Sprintf("%v", key) + `,`,
				`Values:` + fmt.Sprintf("%v", values) + `,`,
				`}`,
			}, "")
			repeatedStringForHeaders += s + ","
		}
		repeatedStringForHeaders += "}"
		s := strings.Join([]string{`http.Request{`,
			`Method:` + fmt.Sprintf("%v", httpReq.Method) + `,`,
			`Url:` + fmt.Sprintf("%v", httpReq.URL) + `,`,
			`Headers:` + repeatedStringForHeaders + `,`,
			`}`,
		}, "")
		return s
	}
	return fmt.Sprintf("%T", req)
}
