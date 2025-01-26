package grpcutil

import (
	"context"
	"strconv"

	"google.golang.org/grpc/metadata"
)

const (
	// MetadataMessageSize is grpc metadata key for message size.
	MetadataMessageSize = "message-size"
	// MetadataCluster is grpc metadata key for cluster name.
	MetadataCluster = "x-cluster"
)

// Sizer can return its size in bytes.
type Sizer interface {
	Size() int
}

func AppendMessageSizeToOutgoingContext(ctx context.Context, req Sizer) context.Context {
	return metadata.AppendToOutgoingContext(ctx, MetadataMessageSize, strconv.Itoa(req.Size()))
}

func AppendClusterToOutgoungContext(ctx context.Context, cluster string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, MetadataCluster, cluster)
}

func GetClusterFromIncomingContext(ctx context.Context) (string, bool) {
	clusterIDs := metadata.ValueFromIncomingContext(ctx, MetadataCluster)
	if len(clusterIDs) != 1 {
		return "", false
	}
	return clusterIDs[0], true
}
