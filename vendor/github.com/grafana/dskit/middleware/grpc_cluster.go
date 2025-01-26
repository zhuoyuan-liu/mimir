package middleware

import (
	"context"
	"fmt"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/gogo/status"
	"github.com/grafana/dskit/grpcutil"
	"go.uber.org/atomic"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

var (
	clusterUnaryServerCount = atomic.NewInt64(0)
)

func ClusterUnaryClientInterceptor(cluster string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if cluster != "" {
			ctx = grpcutil.AppendClusterToOutgoungContext(ctx, cluster)
		}

		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func ClusterUnaryServerInterceptor(cluster string, logger log.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		reqCluster, ok := grpcutil.GetClusterFromIncomingContext(ctx)
		if ok {
			if reqCluster != cluster {
				msg := fmt.Sprintf("request intended for cluster %q - this is cluster %q", reqCluster, cluster)
				level.Error(logger).Log("msg", msg)
				return nil, status.Error(codes.FailedPrecondition, msg)
			}
		}
		if clusterUnaryServerCount.Inc()%1000 == 0 {
			if !ok {
				reqCluster = "<unknown>"
			}
			level.Info(logger).Log("server", info.Server, "method", info.FullMethod, "msg", "Cluster verifier", "req", requestToString(req), "context", contextToString(ctx), "serverCluster", cluster, "requestCluster", reqCluster)
		}
		return handler(ctx, req)
	}
}
