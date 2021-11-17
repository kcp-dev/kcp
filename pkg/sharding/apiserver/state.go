package apiserver

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/storage/etcd3"
	"k8s.io/klog/v2"
)

// ShardedChunkedStates stores values we use to keep track of what shards we're
// contacting for a chunked LIST request during fan-out and where we're at with
// those delegates. This data is either encoded in a client's continue query.
type ShardedChunkedStates struct {
	// ShardResourceVersion is the version at which the list of shards
	// that needed to be queried was resolved.
	ShardResourceVersion int64 `json:"srv"`
	// ResourceVersions hold state for individual shards being queried.
	ResourceVersions []ShardedChunkedState `json:"rvs"`
}

// Decode decodes a continue token into state
func (s *ShardedChunkedStates) Decode(encoded string) error {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("invalid sharded continue encoding: %w", err)
	}
	klog.V(10).Infof("parsed continue=%s", string(raw))
	if err := json.Unmarshal(raw, s); err != nil {
		return fmt.Errorf("invalid sharded continue serialization: %w", err)
	}
	return nil
}

// Encode encodes into a continue token
func (s *ShardedChunkedStates) Encode() (string, error) {
	finished := true
	for _, shard := range s.ResourceVersions {
		finished = finished && shard.Exhausted()
	}
	if finished {
		// all shards exhausted, nothing more to do
		klog.Info("encoded continue=''")
		return "", nil
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	klog.V(10).Infof("encoded continue=%s", string(raw))
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// ToResourceVersions collapses the chunked state to a minimal state for versioning
func (s *ShardedChunkedStates) ToResourceVersions() *ShardedResourceVersions {
	var shards []ShardedResourceVersion
	for _, shard := range s.ResourceVersions {
		if shard.ResourceVersion != 0 {
			shards = append(shards, ShardedResourceVersion{
				Identifier:      shard.Identifier,
				ResourceVersion: shard.ResourceVersion,
			})
		}
	}
	return &ShardedResourceVersions{
		ShardResourceVersion: s.ShardResourceVersion,
		ResourceVersions:     shards,
	}
}

// UpdateWith updates the state from a new request from a shard
func (s *ShardedChunkedStates) UpdateWith(identifier string, resp metav1.ListInterface) error {
	index := -1
	for i, shard := range s.ResourceVersions {
		if shard.Identifier == identifier {
			index = i
		}
	}
	if index == -1 {
		return fmt.Errorf("no such shard %q", identifier)
	}
	if continueValue := resp.GetContinue(); continueValue != "" {
		// the client should continue paging from this shard, it has more data left
		startKey, version, err := etcd3.DecodeContinue(continueValue, "")
		if err != nil {
			return fmt.Errorf("got invalid continue value from delegate: %w", err)
		}
		s.ResourceVersions[index].StartKey = startKey
		s.ResourceVersions[index].ResourceVersion = version
	} else {
		// the client has finished paging from this shard
		version, err := etcd3.Versioner.ParseResourceVersion(resp.GetResourceVersion())
		if err != nil {
			return fmt.Errorf("got invalid resource version value from delegate: %w", err)
		}
		s.ResourceVersions[index].StartKey = ""
		s.ResourceVersions[index].ResourceVersion = int64(version)
	}
	return nil
}

// NextQuery determines the shard identifier and query parameters we should use for the next query
func (s *ShardedChunkedStates) NextQuery() (string, string, error) {
	index := -1
	// prefer to finish chunking a shard that's already midway
	for i, shard := range s.ResourceVersions {
		if shard.Active() {
			index = i
			break
		}
	}
	if index == -1 {
		// otherwise, start chunking a new shard
		for i, shard := range s.ResourceVersions {
			if shard.Pending() {
				index = i
				break
			}
		}
	}
	if index != -1 {
		continueToken, err := s.ResourceVersions[index].ContinueToken()
		return s.ResourceVersions[index].Identifier, continueToken, err
	}
	// or, we're done here ... the client is doing something odd
	return "", "", nil
}

// ShardedChunkedState holds state between queries for an individual shard during fan-out.
type ShardedChunkedState struct {
	// Identifier is used to locate credentials for this shard
	Identifier string `json:"id"`
	// ResourceVersion is set once we've queried the shard successfully once
	ResourceVersion int64 `json:"rv,omitempty"`
	// StartKey is set when we are chunking from this shard
	StartKey string `json:"start,omitempty"`
}

// Pending determines if this shard has not been chunked yet
func (s *ShardedChunkedState) Pending() bool {
	return s.ResourceVersion == 0 && s.StartKey == ""
}

// Active determines if this shard is being actively chunked
func (s *ShardedChunkedState) Active() bool {
	return s.ResourceVersion != 0 && s.StartKey != ""
}

// Exhausted determines if this shard has been fully chunked
func (s *ShardedChunkedState) Exhausted() bool {
	return s.ResourceVersion != 0 && s.StartKey == ""
}

// sillyPrefix works around validation in the storage library
const sillyPrefix = "_"

// ContinueToken encodes shard-specific continue tokens and resource versions for chunking
func (s *ShardedChunkedState) ContinueToken() (string, error) {
	if !s.Active() {
		return "", nil
	}
	return etcd3.EncodeContinue(sillyPrefix+s.StartKey, sillyPrefix, s.ResourceVersion)
}

// ShardedResourceVersions are what a client passes to the resourceVersion
// query parameter to initiate a LIST or WATCH across shards at a particular
// point in time.
type ShardedResourceVersions struct {
	// ShardResourceVersion is the version at which the list of shards
	// that needed to be queried was resolved.
	ShardResourceVersion int64 `json:"srv"`
	// ResourceVersions hold versions for individual shards being queried.
	ResourceVersions []ShardedResourceVersion `json:"rvs"`
}

type ShardedResourceVersion struct {
	// Identifier is used to locate credentials for this shard
	Identifier      string `json:"id"`
	ResourceVersion int64  `json:"rv"`
}

// Decode decodes a resource version into state
func (s *ShardedResourceVersions) Decode(encoded string) error {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("invalid sharded resource version encoding: %w", err)
	}
	klog.V(10).Infof("parsed resourceVersion=%s", string(raw))
	if err := json.Unmarshal(raw, s); err != nil {
		return fmt.Errorf("invalid sharded resource version serialization: %w", err)
	}
	return nil
}

// Encode encodes into a resource version token
func (s *ShardedResourceVersions) Encode() (string, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	klog.V(10).Infof("encoded resourceVersion=%s", string(raw))
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// UpdateWith updates the state from a new event from a shard
func (s *ShardedResourceVersions) UpdateWith(identifier string, resp metav1.Common) error {
	index := -1
	for i, shard := range s.ResourceVersions {
		if shard.Identifier == identifier {
			index = i
		}
	}
	if index == -1 {
		return fmt.Errorf("no such shard %q", identifier)
	}
	updated, err := resourceVersionFor(identifier, resp)
	if err != nil {
		return err
	}
	klog.V(10).Infof("updated resourceVersion[%s]=%d", identifier, updated.ResourceVersion)
	s.ResourceVersions[index] = updated
	return nil
}

func resourceVersionFor(identifier string, resp metav1.Common) (ShardedResourceVersion, error) {
	resourceVersion := resp.GetResourceVersion()
	if resourceVersion == "" {
		return ShardedResourceVersion{}, fmt.Errorf("object had no resource version: %T", resp)
	}
	version, err := strconv.ParseInt(resourceVersion, 10, 64)
	if err != nil {
		return ShardedResourceVersion{}, fmt.Errorf("object had invalid resource version: %s: %v", resourceVersion, err)
	}
	return ShardedResourceVersion{
		Identifier:      identifier,
		ResourceVersion: version,
	}, nil
}

// Append adds an entry in the state from a new event from a shard
func (s *ShardedResourceVersions) Append(identifier string, resp metav1.Common) error {
	version, err := resourceVersionFor(identifier, resp)
	if err != nil {
		return err
	}
	s.ResourceVersions = append(s.ResourceVersions, version)
	return nil
}

// NewChunkedState parses state from a user query or initializes it if the client did not
// request anything specific.
func NewChunkedState(encodedContinueTokens string, identifiers []string, shardResourceVersion int64) (*ShardedChunkedStates, error) {
	if encodedContinueTokens == "" {
		var shards []ShardedChunkedState
		for _, identifier := range identifiers {
			shards = append(shards, ShardedChunkedState{Identifier: identifier})
		}
		return &ShardedChunkedStates{
			ShardResourceVersion: shardResourceVersion,
			ResourceVersions:     shards,
		}, nil
	} else {
		state := &ShardedChunkedStates{}
		err := state.Decode(encodedContinueTokens)
		return state, err
	}
}

// NewResourceVersionState parses state from a user query or initializes it if the client did not
// request anything specific.
func NewResourceVersionState(encodedResourceVersion string, identifiers []string, shardResourceVersion int64) (*ShardedResourceVersions, error) {
	if encodedResourceVersion == "" {
		var shards []ShardedResourceVersion
		for _, identifier := range identifiers {
			shards = append(shards, ShardedResourceVersion{Identifier: identifier})
		}
		return &ShardedResourceVersions{
			ShardResourceVersion: shardResourceVersion,
			ResourceVersions:     shards,
		}, nil
	} else {
		state := &ShardedResourceVersions{}
		err := state.Decode(encodedResourceVersion)
		if err != nil {
			return nil, err
		}
		// TODO: is this really sane to have semi-provided RVs?
		if state.ShardResourceVersion == 0 {
			// the client gave us some partial state, we fill it im
			state.ShardResourceVersion = shardResourceVersion
			for _, identifier := range identifiers {
				found := false
				for _, provided := range state.ResourceVersions {
					if provided.Identifier == identifier {
						found = true
						break
					}
				}
				if !found {
					state.ResourceVersions = append(state.ResourceVersions, ShardedResourceVersion{Identifier: identifier})
				}
			}
		}
		return state, err
	}
}
