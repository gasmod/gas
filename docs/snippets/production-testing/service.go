// Package visits is the service the testing page tests.
package visits

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gasmod/gas"
)

// #region service
// Service counts page visits in the cache. It depends on gas.CacheProvider,
// not on a cache backend, so a test can hand it a mock.
type Service struct {
	cache  gas.CacheProvider
	router *gas.Router
}

func New(cache gas.CacheProvider, router *gas.Router) *Service {
	return &Service{cache: cache, router: router}
}

func (s *Service) Name() string                { return "visits" }
func (s *Service) Close(context.Context) error { return nil }

func (s *Service) Init(context.Context) error {
	s.router.Post(s, "/visits/{page}", s.record)
	return nil
}

// Visit increments the counter for page and returns the new count.
func (s *Service) Visit(ctx context.Context, page string) (int, error) {
	key := "visits:" + page

	n := 0
	raw, err := s.cache.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	if raw != nil {
		if n, err = strconv.Atoi(string(raw)); err != nil {
			return 0, errors.New("corrupt counter for " + page)
		}
	}

	n++
	if err := s.cache.Set(ctx, key, []byte(strconv.Itoa(n)), 24*time.Hour); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Service) record(ctx gas.Context) error {
	n, err := s.Visit(ctx, ctx.Param("page"))
	if err != nil {
		return gas.ServiceUnavailable("visit counter unavailable").WithCause(err)
	}
	return ctx.JSON(http.StatusOK, map[string]int{"visits": n})
}

// #endregion service
