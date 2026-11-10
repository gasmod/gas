package valkey_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/cache"
	"github.com/gasmod/gas/cache/internal/conformance"
	"github.com/gasmod/gas/cache/valkey"
)

// The store keeps the contract of the cache package on a cluster, whose
// nodes each serve a part of the keys.
func TestCluster_Conformance(t *testing.T) {
	t.Parallel()

	addr := valkeyCluster(t).addr

	conformance.Run(t, conformance.Subject{
		Name: valkey.ServiceName,
		New: func(t *testing.T, defaultTTL time.Duration) conformance.Store {
			t.Helper()

			cfg := config(addr)

			cfg.Cache.DefaultTTL = defaultTTL

			s, _ := build(t, valkey.WithConfig(cfg))

			return s
		},
	})
}

// Given the address of one node, the store reaches the keys every node
// serves, under its key prefix, and is ready; after Close it is closed.
func TestCluster_KeysOnEveryNode(t *testing.T) {
	t.Parallel()

	cfg := config(valkeyCluster(t).addr)
	s, _ := build(t, valkey.WithConfig(cfg))
	ctx := t.Context()

	require.NoError(t, s.Init(ctx))

	keys := keyPerNode(t, cfg.Cache.KeyPrefix)

	for _, key := range keys {
		require.NoError(t, s.Set(ctx, key, []byte("v-"+key), time.Minute), key)

		got, err := s.Get(ctx, key)
		require.NoError(t, err, key)
		require.Equal(t, []byte("v-"+key), got, key)

		c := s.Client()

		raw, err := c.Do(ctx, c.B().Get().Key(cfg.Cache.KeyPrefix+key).Build()).ToString()
		require.NoError(t, err, key)
		require.Equal(t, "v-"+key, raw, "the server holds the key under the prefix")

		ok, err := s.Exists(ctx, key)
		require.NoError(t, err, key)
		require.True(t, ok, key)

		require.NoError(t, s.Delete(ctx, key), key)

		_, err = s.Get(ctx, key)
		require.ErrorIs(t, err, cache.ErrKeyNotFound, key)
	}

	require.NoError(t, s.CheckReady(ctx))
	require.NoError(t, s.Close(ctx))

	_, err := s.Get(ctx, keys[0])
	require.ErrorIs(t, err, cache.ErrClosed)
}

// The ACL user authenticates on every node the store connects to.
func TestCluster_ACLUser(t *testing.T) {
	t.Parallel()

	cfg := config(valkeyCluster(t).addr)

	cfg.Cache.Username = aclUser
	cfg.Cache.Password = aclPassword

	s := open(t, cfg)
	ctx := t.Context()

	for _, key := range keyPerNode(t, cfg.Cache.KeyPrefix) {
		require.NoError(t, s.Set(ctx, key, []byte("v"), time.Minute), key)
	}
}

// A user that may not run CLUSTER connects, but valkey-go cannot read the
// cluster's slots, so the store talks to the node at Addr alone: a key
// that node serves works, and a key another node serves fails with
// MOVED.
func TestCluster_UserWithoutCluster(t *testing.T) {
	t.Parallel()

	cfg := config(valkeyCluster(t).addr)

	cfg.Cache.Username = noClusterUser
	cfg.Cache.Password = aclPassword

	s := open(t, cfg)
	ctx := t.Context()
	keys := keyPerNode(t, cfg.Cache.KeyPrefix)

	require.NoError(t, s.Set(ctx, keys[0], []byte("v"), time.Minute))

	for _, key := range keys[1:] {
		err := s.Set(ctx, key, []byte("v"), time.Minute)
		require.ErrorIs(t, err, valkey.ErrCommand)
		require.EqualError(t, err, "gas/cache/valkey: set: MOVED")
	}
}

// A cluster of valkey-server 8 has only database 0: a store with another
// fails Init, as with a database number a server does not have.
func TestCluster_DBNotZero(t *testing.T) {
	t.Parallel()

	cfg := config(valkeyCluster(t).addr)

	cfg.Cache.DB = 1

	err := initErr(t, valkey.WithConfig(cfg))
	require.ErrorIs(t, err, valkey.ErrConnect)
	require.EqualError(t, err, "gas/cache/valkey: connect: failed")
}

// keyPerNode returns a key for each node of the cluster, in clusterNodes
// order, whose slot, with prefix, the node serves, as CLUSTER KEYSLOT
// reports it to the default user.
func keyPerNode(t *testing.T, prefix string) []string {
	t.Helper()

	c := open(t, config(valkeyCluster(t).addr)).Client()
	keys := make([]string, len(clusterNodes))
	found := 0

	for i := 0; found < len(clusterNodes); i++ {
		require.Less(t, i, 1000, "no key for every node")

		key := "k" + strconv.Itoa(i)

		slot, err := c.Do(t.Context(), c.B().ClusterKeyslot().Key(prefix+key).Build()).
			AsInt64()
		require.NoError(t, err)

		for n, node := range clusterNodes {
			last, err := strconv.ParseInt(node.last, 10, 64)
			require.NoError(t, err)

			first, err := strconv.ParseInt(node.first, 10, 64)
			require.NoError(t, err)

			if keys[n] == "" && slot >= first && slot <= last {
				keys[n] = key
				found++
			}
		}
	}

	return keys
}
