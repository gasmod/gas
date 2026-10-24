// Package main shows gas/cache wiring and the cache-aside pattern.
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
		// Both backends resolve these two from the container.
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),

		// Services depend on gas.CacheProvider, not on a backend.
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

// #region memory-config
func boundedMemory() gas.Option {
	cfg := cachemem.DefaultConfig()
	cfg.Cache.MaxEntries = 10_000
	cfg.Cache.DefaultTTL = 10 * time.Minute

	return gas.WithSingletonService[gas.CacheProvider](
		cachemem.New(cachemem.WithConfig(cfg)),
	)
}

// #endregion memory-config

// #region cache-aside
type Profiles struct {
	cache  gas.CacheProvider
	logger gas.Logger
}

func NewProfiles(c gas.CacheProvider, logger gas.Logger) *Profiles {
	return &Profiles{cache: c, logger: logger}
}

// Profile reads through the cache, falls back to the source of truth, then
// populates the cache.
func (s *Profiles) Profile(ctx context.Context, id string) (*Profile, error) {
	key := "profile:" + id

	raw, err := s.cache.Get(ctx, key)
	switch {
	case err == nil:
		var p Profile
		if err := json.Unmarshal(raw, &p); err == nil {
			return &p, nil
		}
		// A corrupt entry is replaced below.
		s.logger.Warn("corrupt profile cache entry").Str("key", key).Err("error", err).Send()
	case errors.Is(err, cache.ErrKeyNotFound):
		// A miss: load and populate.
	default:
		// A broken cache surfaces instead of turning into load on the database.
		return nil, err
	}

	p, err := s.loadProfile(ctx, id)
	if err != nil {
		return nil, err
	}

	raw, err = json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if err := s.cache.Set(ctx, key, raw, 5*time.Minute); err != nil {
		// The read succeeded; a failed populate only costs the next reader a miss.
		s.logger.Warn("cache populate failed").Str("key", key).Err("error", err).Send()
	}
	return p, nil
}

// #endregion cache-aside

// #region invalidate
func (s *Profiles) Rename(ctx context.Context, id, name string) error {
	if err := s.saveName(ctx, id, name); err != nil {
		return err
	}
	// Delete rather than overwrite: the next read repopulates from the
	// source of truth, so a failed write cannot leave a stale entry behind.
	return s.cache.Delete(ctx, "profile:"+id)
}

// #endregion invalidate

// #region valkey-client
// Hits counts page views with INCR, a command gas.CacheProvider does not
// cover. It needs the Valkey backend.
func Hits(ctx context.Context, c gas.CacheProvider, page string) (int64, error) {
	vk, ok := c.(*cachevk.Service)
	if !ok {
		return 0, errors.New("hit counter needs the valkey cache backend")
	}
	client := vk.Client()
	if client == nil {
		return 0, cache.ErrClosed
	}
	return client.Do(ctx, client.B().Incr().Key("hits:"+page).Build()).AsInt64()
}

// #endregion valkey-client

type Profile struct {
	ID   string
	Name string
}

func (s *Profiles) loadProfile(_ context.Context, id string) (*Profile, error) {
	return &Profile{ID: id}, nil
}

func (s *Profiles) saveName(_ context.Context, _, _ string) error { return nil }
