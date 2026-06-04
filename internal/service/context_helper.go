package service

import (
	"context"
	"strconv"
	"strings"

	"google.golang.org/grpc/metadata"
)

// The admin gateway injects caller identity as gRPC metadata headers.
func getTenantID(ctx context.Context) uint32 {
	return mdUint32(ctx, "x-md-global-tenantid")
}

func getUserID(ctx context.Context) uint32 {
	return mdUint32(ctx, "x-md-global-userid")
}

func getUsername(ctx context.Context) string {
	return mdString(ctx, "x-md-global-username")
}

func getRoles(ctx context.Context) []string {
	v := mdString(ctx, "x-md-global-roles")
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}

func mdString(ctx context.Context, key string) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(key)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

func mdUint32(ctx context.Context, key string) uint32 {
	s := mdString(ctx, key)
	if s == "" {
		return 0
	}
	id, _ := strconv.ParseUint(s, 10, 32)
	return uint32(id)
}
