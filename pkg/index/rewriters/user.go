package rewriters

import (
	"crypto/sha256"
	"strings"

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/martinlindhe/base36"
)

// UserRewriter translates a user home cluster path: "user:paul:abc" to "<cluster>:abc".
func UserRewriter(segments []string) []string {
	if segments[0] == "user" && len(segments) > 1 {
		return append(strings.Split(HomeClusterName(segments[1]).String(), ":"), segments[2:]...)
	}

	return segments
}

func HomeClusterName(userName string) logicalcluster.Name {
	hash := sha256.Sum224([]byte(userName))
	return logicalcluster.New(strings.ToLower(base36.EncodeBytes(hash[:8])))
}
