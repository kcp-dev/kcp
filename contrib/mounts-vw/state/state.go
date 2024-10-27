package state

import (
	"sync"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// ClientSetStoreInterface defines the interface for storing and retrieving Kubernetes clientsets
type ClientSetStoreInterface interface {
	Set(key string, value Value)
	Get(key string) (Value, bool)
	Delete(key string)
}

type Value struct {
	Client *kubernetes.Clientset
	Config *rest.Config
	URL    string
}

// ClientSetStore provides a thread-safe in-memory store for Kubernetes clientsets
type ClientSetStore struct {
	store map[string]Value
	mu    sync.RWMutex
}

// NewClientSetStore initializes a new ClientSetStore
func NewClientSetStore() *ClientSetStore {
	return &ClientSetStore{
		store: make(map[string]Value),
	}
}

// Set stores a clientset with the associated key, ensuring write-locking for thread safety
func (c *ClientSetStore) Set(key string, value Value) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store[key] = value
}

// Get retrieves the clientset associated with the key, ensuring read-locking for thread safety
func (c *ClientSetStore) Get(key string) (Value, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, exists := c.store[key]
	return v, exists
}

// Delete removes a clientset associated with the key, if it exists
func (c *ClientSetStore) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.store, key)
}
