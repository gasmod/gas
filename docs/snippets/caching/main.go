// Package main shows caching with gas/cache.
package main

// #region imports
import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache"
	cachemem "github.com/gasmod/gas/cache/memory"
	cachevk "github.com/gasmod/gas/cache/valkey"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	gaslog "github.com/gasmod/gas/log"
)

// #endregion imports

func main() {
	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}

	// #region wiring
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),

		// Swap cachemem.New() for cachevk.New() and nothing else changes:
		// services depend on gas.CacheProvider, not on either backend.
		gas.WithSingletonService[gas.CacheProvider](cachemem.New()),
	)
	// #endregion wiring

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// #region valkey
func production() gas.Option {
	return gas.WithSingletonService[gas.CacheProvider](cachevk.New())
}

// #endregion valkey

// #region cache-aside
type Service struct {
	cache gas.CacheProvider
	db    gas.DatabaseProvider
}

// Read through the cache, fall back to the database, then populate. A cache
// miss is a sentinel error, not a nil value, so an empty result stays
// distinguishable from an absent one.
func (s *Service) Profile(ctx context.Context, id string) (*Profile, error) {
	key := "profile:" + id

	if raw, err := s.cache.Get(ctx, key); err == nil {
		var p Profile
		if json.Unmarshal(raw, &p) == nil {
			return &p, nil
		}
		// A corrupt entry is not fatal; fall through and refresh it.
	} else if !errors.Is(err, cache.ErrKeyNotFound) {
		return nil, err
	}

	p, err := s.loadProfile(ctx, id)
	if err != nil {
		return nil, err
	}

	if raw, err := json.Marshal(p); err == nil {
		_ = s.cache.Set(ctx, key, raw, 5*time.Minute)
	}
	return p, nil
}

// #endregion cache-aside

// #region invalidate
func (s *Service) Rename(ctx context.Context, id, name string) error {
	if err := s.saveName(ctx, id, name); err != nil {
		return err
	}
	// Delete rather than overwrite: the next read repopulates from the
	// source of truth, so a failed write cannot leave a stale entry behind.
	return s.cache.Delete(ctx, "profile:"+id)
}

// #endregion invalidate

type Profile struct {
	ID   string
	Name string
}

func (s *Service) loadProfile(_ context.Context, id string) (*Profile, error) {
	return &Profile{ID: id}, nil
}
func (s *Service) saveName(_ context.Context, _, _ string) error { return nil }
