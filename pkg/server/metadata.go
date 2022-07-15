package server

import (
	"context"

	"github.com/munnerz/goautoneg"
)

func isPartialMetadataRequest(ctx context.Context) bool {
	accept := ctx.Value(acceptHeaderContextKey).(string)
	if accept == "" {
		return false
	}

	return isPartialMetadataHeader(accept)
}

func isPartialMetadataHeader(accept string) bool {
	clauses := goautoneg.ParseAccept(accept)
	for _, clause := range clauses {
		if clause.Params["as"] == "PartialObjectMetadata" || clause.Params["as"] == "PartialObjectMetadataList" {
			return true
		}
	}

	return false
}
