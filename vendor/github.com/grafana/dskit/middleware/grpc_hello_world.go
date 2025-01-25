package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/httpgrpc"
	"go.uber.org/atomic"
	"google.golang.org/grpc"
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

func HelloWorldUnaryServerInterceptor(logger log.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if unaryCount.Inc()%1000 == 0 {
			level.Info(logger).Log("server", info.Server, "method", info.FullMethod, "msg", "Hello, World!!!", "req", requestToString(req))
		}
		return handler(ctx, req)
	}
}

func HelloWorldStreamServerInterceptor(logger log.Logger) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if streamCount.Inc()%1000 == 0 {
			streamType := make([]string, 0, 2)
			if info.IsServerStream {
				streamType = append(streamType, "stream")
			}
			if info.IsClientStream {
				streamType = append(streamType, "client")
			}
			level.Info(logger).Log("stream", streamType, "method", info.FullMethod, "msg", "Hello, World!!!")
		}
		return handler(srv, ss)
	}
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
	httpReq, ok := req.(*http.Request)
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
		s := strings.Join([]string{`&http.Request{`,
			`Method:` + fmt.Sprintf("%v", httpReq.Method) + `,`,
			`Url:` + fmt.Sprintf("%v", httpReq.URL) + `,`,
			`Headers:` + repeatedStringForHeaders + `,`,
			`}`,
		}, "")
		return s
	}
	httpReq1, ok := req.(http.Request)
	if ok {
		repeatedStringForHeaders := "[]*Header{"
		for key, values := range httpReq1.Header {
			s := strings.Join([]string{`&Header{`,
				`Key:` + fmt.Sprintf("%v", key) + `,`,
				`Values:` + fmt.Sprintf("%v", values) + `,`,
				`}`,
			}, "")
			repeatedStringForHeaders += s + ","
		}
		repeatedStringForHeaders += "}"
		s := strings.Join([]string{`http.Request{`,
			`Method:` + fmt.Sprintf("%v", httpReq1.Method) + `,`,
			`Url:` + fmt.Sprintf("%v", httpReq1.URL) + `,`,
			`Headers:` + repeatedStringForHeaders + `,`,
			`}`,
		}, "")
		return s
	}
	return fmt.Sprintf("%t", req)
}
