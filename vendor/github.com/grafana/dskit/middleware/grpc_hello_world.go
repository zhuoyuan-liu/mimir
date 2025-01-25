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
	unaryCount  = atomic.NewInt64(0)
	streamCount = atomic.NewInt64(0)
)

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
		return httpGrpcReq.String()
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
			`Body:` + fmt.Sprintf("%v", httpReq.Body) + `,`,
			`}`,
		}, "")
		return s
	}
	return "unknown"
}
