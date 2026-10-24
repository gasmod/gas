// Package main shows replacing a Gas module with an implementation of your own.
package main

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/gasmod/gas"
)

// #region swap
// MapCache is an in-house gas.CacheProvider. Nothing about it is specific
// to Gas beyond the four methods of the interface.
type MapCache struct {
	mu   sync.Mutex
	data map[string][]byte
}

var _ gas.CacheProvider = (*MapCache)(nil)

var errMiss = errors.New("cache miss")

func (c *MapCache) Get(_ context.Context, key string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.data[key]
	if !ok {
		return nil, errMiss
	}
	return v, nil
}

func (c *MapCache) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = value
	return nil
}

func (c *MapCache) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data, key)
	return nil
}

func (c *MapCache) Exists(_ context.Context, key string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.data[key]
	return ok, nil
}

func main() {
	app := gas.NewApp(
		// Registered under the core interface, where gas/cache would be.
		// Every service that takes gas.CacheProvider now gets MapCache.
		gas.WithServiceInstance[gas.CacheProvider](&MapCache{data: map[string][]byte{}}),
	)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// #endregion swap
