package valkey

import (
	"context"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/gasmod/gas/cache"
)

// Get returns the value of KeyPrefix and key, or an error wrapping
// cache.ErrKeyNotFound when the server holds none. An empty value comes
// back as an empty, non-nil slice.
func (s *Service) Get(ctx context.Context, key string) ([]byte, error) {
	err := cache.CheckKey(key)
	if err != nil {
		return nil, opError(opGet, err)
	}

	st, err := s.loaded(ctx, opGet)
	if err != nil {
		return nil, err
	}

	resp, err := s.do(ctx, st, opGet, st.client.B().Get().Key(st.prefix+key).Build())
	if err != nil {
		return nil, err
	}

	// valkey-go's AsBytes returns a slice over the reply's string, not a
	// copy, so the value is copied out of ToString's result instead.
	str, err := resp.ToString()
	if valkey.IsValkeyNil(err) {
		return nil, opError(opGet, cache.ErrKeyNotFound)
	}

	if err != nil {
		return nil, commandError(ctx, opGet, err)
	}

	return append([]byte{}, str...), nil
}

// Set stores value under KeyPrefix and key, with SET and, when the value
// expires, PX, as described in the package documentation. A nil value is
// stored as empty. A Set without expiry clears the TTL a key had.
func (s *Service) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	err := cache.CheckKey(key)
	if err != nil {
		return opError(opSet, err)
	}

	err = cache.CheckTTL(ttl)
	if err != nil {
		return opError(opSet, err)
	}

	st, err := s.loaded(ctx, opSet)
	if err != nil {
		return err
	}

	set := st.client.B().Set().Key(st.prefix + key).Value(string(value))

	var cmd valkey.Completed

	if d := cache.EffectiveTTL(ttl, st.defaultTTL); d > 0 {
		cmd = set.PxMilliseconds(d.Milliseconds()).Build()
	} else {
		cmd = set.Build()
	}

	_, err = s.do(ctx, st, opSet, cmd)

	return err
}

// Delete removes the value of KeyPrefix and key, with DEL. Deleting a key
// the server does not hold is not an error.
func (s *Service) Delete(ctx context.Context, key string) error {
	err := cache.CheckKey(key)
	if err != nil {
		return opError(opDelete, err)
	}

	st, err := s.loaded(ctx, opDelete)
	if err != nil {
		return err
	}

	_, err = s.do(ctx, st, opDelete, st.client.B().Del().Key(st.prefix+key).Build())

	return err
}

// Exists reports whether the server holds a value under KeyPrefix and
// key, with EXISTS.
func (s *Service) Exists(ctx context.Context, key string) (bool, error) {
	err := cache.CheckKey(key)
	if err != nil {
		return false, opError(opExists, err)
	}

	st, err := s.loaded(ctx, opExists)
	if err != nil {
		return false, err
	}

	resp, err := s.do(ctx, st, opExists, st.client.B().Exists().Key(st.prefix+key).Build())
	if err != nil {
		return false, err
	}

	n, err := resp.AsInt64()
	if err != nil {
		return false, commandError(ctx, opExists, err)
	}

	return n > 0, nil
}
