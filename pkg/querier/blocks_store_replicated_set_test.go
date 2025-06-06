// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/cortexproject/cortex/blob/master/pkg/querier/blocks_store_replicated_set_test.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Cortex Authors.

package querier

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/grafana/dskit/flagext"
	"github.com/grafana/dskit/grpcclient"
	"github.com/grafana/dskit/kv/consul"
	"github.com/grafana/dskit/ring"
	"github.com/grafana/dskit/services"
	"github.com/grafana/dskit/test"
	"github.com/oklog/ulid/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mimir_tsdb "github.com/grafana/mimir/pkg/storage/tsdb"
	"github.com/grafana/mimir/pkg/storage/tsdb/bucketindex"
	"github.com/grafana/mimir/pkg/storegateway"
)

func newBlock(id ulid.ULID, minT time.Time, maxT time.Time) *bucketindex.Block {
	return &bucketindex.Block{
		ID:      id,
		MinTime: minT.UnixMilli(),
		MaxTime: maxT.UnixMilli(),
	}
}

func TestBlocksStoreReplicationSet_GetClientsFor(t *testing.T) {
	// The following block IDs have been picked to have increasing hash values
	// in order to simplify the tests.
	blockID1 := ulid.MustNew(1, nil) // hash: 283204220
	blockID2 := ulid.MustNew(2, nil) // hash: 444110359
	blockID3 := ulid.MustNew(5, nil) // hash: 2931974232
	blockID4 := ulid.MustNew(6, nil) // hash: 3092880371

	block1Hash := mimir_tsdb.HashBlockID(blockID1)
	block2Hash := mimir_tsdb.HashBlockID(blockID2)
	block3Hash := mimir_tsdb.HashBlockID(blockID3)
	block4Hash := mimir_tsdb.HashBlockID(blockID4)

	minT := time.Now().Add(-5 * time.Hour)
	maxT := minT.Add(2 * time.Hour)

	block1 := newBlock(blockID1, minT, maxT)
	block2 := newBlock(blockID2, minT, maxT)
	block3 := newBlock(blockID3, minT, maxT)
	block4 := newBlock(blockID4, minT, maxT)

	userID := "user-A"
	registeredAt := time.Now()

	tests := map[string]struct {
		tenantShardSize   int
		replicationFactor int
		setup             func(*ring.Desc)
		queryBlocks       bucketindex.Blocks
		exclude           map[ulid.ULID][]string
		expectedClients   map[string][]ulid.ULID
		expectedErr       error
	}{
		"shard size 0, single instance in the ring with RF = 1": {
			tenantShardSize:   0,
			replicationFactor: 1,
			setup: func(d *ring.Desc) {
				d.AddIngester("instance-1", "127.0.0.1", "", []uint32{block1Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
			},
			queryBlocks: []*bucketindex.Block{block1, block2},
			expectedClients: map[string][]ulid.ULID{
				"127.0.0.1": {blockID1, blockID2},
			},
		},
		"shard size 0, single instance in the ring with RF = 1 but excluded": {
			tenantShardSize:   0,
			replicationFactor: 1,
			setup: func(d *ring.Desc) {
				d.AddIngester("instance-1", "127.0.0.1", "", []uint32{block1Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
			},
			queryBlocks: []*bucketindex.Block{block1, block2},
			exclude: map[ulid.ULID][]string{
				blockID1: {"127.0.0.1"},
			},
			expectedErr: fmt.Errorf("no store-gateway instance left after checking exclude for block %s", blockID1.String()),
		},
		"shard size 0, single instance in the ring with RF = 1 but excluded for non queried block": {
			tenantShardSize:   0,
			replicationFactor: 1,
			setup: func(d *ring.Desc) {
				d.AddIngester("instance-1", "127.0.0.1", "", []uint32{block1Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
			},
			queryBlocks: []*bucketindex.Block{block1, block2},
			exclude: map[ulid.ULID][]string{
				blockID3: {"127.0.0.1"},
			},
			expectedClients: map[string][]ulid.ULID{
				"127.0.0.1": {blockID1, blockID2},
			},
		},
		"shard size 0, single instance in the ring with RF = 2": {
			tenantShardSize:   0,
			replicationFactor: 2,
			setup: func(d *ring.Desc) {
				d.AddIngester("instance-1", "127.0.0.1", "", []uint32{block1Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
			},
			queryBlocks: []*bucketindex.Block{block1, block2},
			expectedClients: map[string][]ulid.ULID{
				"127.0.0.1": {blockID1, blockID2},
			},
		},
		"shard size 0, multiple instances in the ring with each requested block belonging to a different store-gateway and RF = 1": {
			tenantShardSize:   0,
			replicationFactor: 1,
			setup: func(d *ring.Desc) {
				d.AddIngester("instance-1", "127.0.0.1", "", []uint32{block1Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-2", "127.0.0.2", "", []uint32{block2Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-3", "127.0.0.3", "", []uint32{block3Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-4", "127.0.0.4", "", []uint32{block4Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
			},
			queryBlocks: []*bucketindex.Block{block1, block3, block4},
			expectedClients: map[string][]ulid.ULID{
				"127.0.0.1": {blockID1},
				"127.0.0.3": {blockID3},
				"127.0.0.4": {blockID4},
			},
		},
		"shard size 0, multiple instances in the ring with each requested block belonging to a different store-gateway and RF = 1 but excluded": {
			tenantShardSize:   0,
			replicationFactor: 1,
			setup: func(d *ring.Desc) {
				d.AddIngester("instance-1", "127.0.0.1", "", []uint32{block1Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-2", "127.0.0.2", "", []uint32{block2Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-3", "127.0.0.3", "", []uint32{block3Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-4", "127.0.0.4", "", []uint32{block4Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
			},
			queryBlocks: []*bucketindex.Block{block1, block3, block4},
			exclude: map[ulid.ULID][]string{
				blockID3: {"127.0.0.3"},
			},
			expectedErr: fmt.Errorf("no store-gateway instance left after checking exclude for block %s", blockID3.String()),
		},
		"shard size 0, multiple instances in the ring with each requested block belonging to a different store-gateway and RF = 2": {
			tenantShardSize:   0,
			replicationFactor: 2,
			setup: func(d *ring.Desc) {
				d.AddIngester("instance-1", "127.0.0.1", "", []uint32{block1Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-2", "127.0.0.2", "", []uint32{block2Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-3", "127.0.0.3", "", []uint32{block3Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-4", "127.0.0.4", "", []uint32{block4Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
			},
			queryBlocks: []*bucketindex.Block{block1, block3, block4},
			expectedClients: map[string][]ulid.ULID{
				"127.0.0.1": {blockID1},
				"127.0.0.3": {blockID3},
				"127.0.0.4": {blockID4},
			},
		},
		"shard size 0, multiple instances in the ring with multiple requested blocks belonging to the same store-gateway and RF = 2": {
			tenantShardSize:   0,
			replicationFactor: 2,
			setup: func(d *ring.Desc) {
				d.AddIngester("instance-1", "127.0.0.1", "", []uint32{block1Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-2", "127.0.0.2", "", []uint32{block3Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
			},
			queryBlocks: []*bucketindex.Block{block1, block2, block3, block4},
			expectedClients: map[string][]ulid.ULID{
				"127.0.0.1": {blockID1, blockID4},
				"127.0.0.2": {blockID2, blockID3},
			},
		},
		"shard size 0, multiple instances in the ring with each requested block belonging to a different store-gateway and RF = 2 and some blocks excluded but with replacement available": {
			tenantShardSize:   0,
			replicationFactor: 2,
			setup: func(d *ring.Desc) {
				d.AddIngester("instance-1", "127.0.0.1", "", []uint32{block1Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-2", "127.0.0.2", "", []uint32{block2Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-3", "127.0.0.3", "", []uint32{block3Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-4", "127.0.0.4", "", []uint32{block4Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
			},
			queryBlocks: []*bucketindex.Block{block1, block3, block4},
			exclude: map[ulid.ULID][]string{
				blockID3: {"127.0.0.3"},
				blockID1: {"127.0.0.1"},
			},
			expectedClients: map[string][]ulid.ULID{
				"127.0.0.2": {blockID1},
				"127.0.0.4": {blockID3, blockID4},
			},
		},
		"shard size 0, multiple instances in the ring are JOINING, the requested block + its replicas only belongs to JOINING instances": {
			tenantShardSize:   0,
			replicationFactor: 2,
			setup: func(d *ring.Desc) {
				d.AddIngester("instance-1", "127.0.0.1", "", []uint32{block1Hash + 1}, ring.JOINING, registeredAt, false, time.Time{})
				d.AddIngester("instance-2", "127.0.0.2", "", []uint32{block2Hash + 1}, ring.JOINING, registeredAt, false, time.Time{})
				d.AddIngester("instance-3", "127.0.0.3", "", []uint32{block3Hash + 1}, ring.JOINING, registeredAt, false, time.Time{})
				d.AddIngester("instance-4", "127.0.0.4", "", []uint32{block4Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
			},
			queryBlocks: []*bucketindex.Block{block1},
			expectedClients: map[string][]ulid.ULID{
				"127.0.0.4": {blockID1},
			},
		},
		"shard size 1, single instance in the ring with RF = 1": {
			tenantShardSize:   1,
			replicationFactor: 1,
			setup: func(d *ring.Desc) {
				d.AddIngester("instance-1", "127.0.0.1", "", []uint32{block1Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
			},
			queryBlocks: []*bucketindex.Block{block1, block2},
			expectedClients: map[string][]ulid.ULID{
				"127.0.0.1": {blockID1, blockID2},
			},
		},
		"shard size 1, single instance in the ring with RF = 1, but store-gateway excluded": {
			tenantShardSize:   1,
			replicationFactor: 1,
			setup: func(d *ring.Desc) {
				d.AddIngester("instance-1", "127.0.0.1", "", []uint32{block1Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
			},
			queryBlocks: []*bucketindex.Block{block1, block2},
			exclude: map[ulid.ULID][]string{
				blockID1: {"127.0.0.1"},
			},
			expectedErr: fmt.Errorf("no store-gateway instance left after checking exclude for block %s", blockID1.String()),
		},
		"shard size 2, single instance in the ring with RF = 2": {
			tenantShardSize:   2,
			replicationFactor: 2,
			setup: func(d *ring.Desc) {
				d.AddIngester("instance-1", "127.0.0.1", "", []uint32{block1Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
			},
			queryBlocks: []*bucketindex.Block{block1, block2},
			expectedClients: map[string][]ulid.ULID{
				"127.0.0.1": {blockID1, blockID2},
			},
		},
		"shard size 1, multiple instances in the ring with RF = 1": {
			tenantShardSize:   1,
			replicationFactor: 1,
			setup: func(d *ring.Desc) {
				d.AddIngester("instance-1", "127.0.0.1", "", []uint32{block1Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-2", "127.0.0.2", "", []uint32{block2Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-3", "127.0.0.3", "", []uint32{block3Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-4", "127.0.0.4", "", []uint32{block4Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
			},
			queryBlocks: []*bucketindex.Block{block1, block2, block4},
			expectedClients: map[string][]ulid.ULID{
				"127.0.0.1": {blockID1, blockID2, blockID4},
			},
		},
		"shard size 2, shuffle sharding, multiple instances in the ring with RF = 1": {
			tenantShardSize:   2,
			replicationFactor: 1,
			setup: func(d *ring.Desc) {
				d.AddIngester("instance-1", "127.0.0.1", "", []uint32{block1Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-2", "127.0.0.2", "", []uint32{block2Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-3", "127.0.0.3", "", []uint32{block3Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-4", "127.0.0.4", "", []uint32{block4Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
			},
			queryBlocks: []*bucketindex.Block{block1, block2, block4},
			expectedClients: map[string][]ulid.ULID{
				"127.0.0.1": {blockID1, blockID4},
				"127.0.0.3": {blockID2},
			},
		},
		"shard size 4, multiple instances in the ring with RF = 1": {
			tenantShardSize:   4,
			replicationFactor: 1,
			setup: func(d *ring.Desc) {
				d.AddIngester("instance-1", "127.0.0.1", "", []uint32{block1Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-2", "127.0.0.2", "", []uint32{block2Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-3", "127.0.0.3", "", []uint32{block3Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-4", "127.0.0.4", "", []uint32{block4Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
			},
			queryBlocks: []*bucketindex.Block{block1, block2, block4},
			expectedClients: map[string][]ulid.ULID{
				"127.0.0.1": {blockID1},
				"127.0.0.2": {blockID2},
				"127.0.0.4": {blockID4},
			},
		},
		"shard size 2, multiple instances in the ring with RF = 2, with excluded blocks but some replacement available": {
			tenantShardSize:   2,
			replicationFactor: 2,
			setup: func(d *ring.Desc) {
				d.AddIngester("instance-1", "127.0.0.1", "", []uint32{block1Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-2", "127.0.0.2", "", []uint32{block2Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-3", "127.0.0.3", "", []uint32{block3Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-4", "127.0.0.4", "", []uint32{block4Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
			},
			queryBlocks: []*bucketindex.Block{block1, block2},
			exclude: map[ulid.ULID][]string{
				blockID1: {"127.0.0.1"},
				blockID2: {"127.0.0.1"},
			},
			expectedClients: map[string][]ulid.ULID{
				"127.0.0.3": {blockID1, blockID2},
			},
		},
		"shard size 2, multiple instances in the ring with RF = 2, SS = 2 with excluded blocks and no replacement available": {
			tenantShardSize:   2,
			replicationFactor: 2,
			setup: func(d *ring.Desc) {
				d.AddIngester("instance-1", "127.0.0.1", "", []uint32{block1Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-2", "127.0.0.2", "", []uint32{block2Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-3", "127.0.0.3", "", []uint32{block3Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
				d.AddIngester("instance-4", "127.0.0.4", "", []uint32{block4Hash + 1}, ring.ACTIVE, registeredAt, false, time.Time{})
			},
			queryBlocks: []*bucketindex.Block{block1, block2},
			exclude: map[ulid.ULID][]string{
				blockID1: {"127.0.0.1", "127.0.0.3"},
				blockID2: {"127.0.0.1"},
			},
			expectedErr: fmt.Errorf("no store-gateway instance left after checking exclude for block %s", blockID1.String()),
		},
	}

	for testName, testData := range tests {
		t.Run(testName, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()

			// Setup the ring state.
			ringStore, closer := consul.NewInMemoryClient(ring.GetCodec(), log.NewNopLogger(), nil)
			t.Cleanup(func() { assert.NoError(t, closer.Close()) })

			require.NoError(t, ringStore.CAS(ctx, "test", func(interface{}) (interface{}, bool, error) {
				d := ring.NewDesc()
				testData.setup(d)
				return d, true, nil
			}))

			ringCfg := ring.Config{}
			flagext.DefaultValues(&ringCfg)
			ringCfg.ReplicationFactor = testData.replicationFactor

			r, err := ring.NewWithStoreClientAndStrategy(ringCfg, "test", "test", ringStore, ring.NewIgnoreUnhealthyInstancesReplicationStrategy(), nil, log.NewNopLogger())
			require.NoError(t, err)
			defer services.StopAndAwaitTerminated(ctx, r) //nolint:errcheck

			limits := &blocksStoreLimitsMock{
				storeGatewayTenantShardSize: testData.tenantShardSize,
			}

			reg := prometheus.NewPedanticRegistry()
			s, err := newBlocksStoreReplicationSet(r, noLoadBalancing, storegateway.NewNopDynamicReplication(ringCfg.ReplicationFactor), limits, grpcclient.Config{}, log.NewNopLogger(), reg)
			require.NoError(t, err)
			require.NoError(t, services.StartAndAwaitRunning(ctx, s))
			defer services.StopAndAwaitTerminated(ctx, s) //nolint:errcheck

			// Wait until the ring client has initialised the state.
			test.Poll(t, time.Second, true, func() interface{} {
				all, err := r.GetAllHealthy(storegateway.BlocksRead)
				return err == nil && len(all.Instances) > 0
			})

			clients, err := s.GetClientsFor(userID, testData.queryBlocks, testData.exclude)
			assert.Equal(t, testData.expectedErr, err)
			defer func() {
				// Close all clients to ensure no goroutines are leaked.
				for c := range clients {
					c.(io.Closer).Close() //nolint:errcheck
				}
			}()

			if testData.expectedErr == nil {
				assert.Equal(t, testData.expectedClients, getStoreGatewayClientAddrs(clients))

				assert.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(fmt.Sprintf(`
					# HELP cortex_storegateway_clients The current number of store-gateway clients in the pool.
					# TYPE cortex_storegateway_clients gauge
					cortex_storegateway_clients{client="querier"} %d
				`, len(testData.expectedClients))), "cortex_storegateway_clients"))
			}
		})
	}
}

func TestBlocksStoreReplicationSet_GetClientsFor_ShouldSupportRandomLoadBalancingStrategy(t *testing.T) {
	const (
		numRuns      = 1000
		numInstances = 3
	)

	ctx := context.Background()
	userID := "user-A"
	registeredAt := time.Now()

	minT := time.Now().Add(-5 * time.Hour)
	maxT := minT.Add(2 * time.Hour)
	blockID1 := ulid.MustNew(1, nil)
	block1 := newBlock(blockID1, minT, maxT)

	// Create a ring.
	ringStore, closer := consul.NewInMemoryClient(ring.GetCodec(), log.NewNopLogger(), nil)
	t.Cleanup(func() { assert.NoError(t, closer.Close()) })

	require.NoError(t, ringStore.CAS(ctx, "test", func(interface{}) (interface{}, bool, error) {
		d := ring.NewDesc()
		for n := 1; n <= numInstances; n++ {
			d.AddIngester(fmt.Sprintf("instance-%d", n), fmt.Sprintf("127.0.0.%d", n), "", []uint32{uint32(n)}, ring.ACTIVE, registeredAt, false, time.Time{})
		}
		return d, true, nil
	}))

	// Configure a replication factor equal to the number of instances, so that every store-gateway gets all blocks.
	ringCfg := ring.Config{}
	flagext.DefaultValues(&ringCfg)
	ringCfg.ReplicationFactor = numInstances

	r, err := ring.NewWithStoreClientAndStrategy(ringCfg, "test", "test", ringStore, ring.NewIgnoreUnhealthyInstancesReplicationStrategy(), nil, log.NewNopLogger())
	require.NoError(t, err)

	limits := &blocksStoreLimitsMock{storeGatewayTenantShardSize: 0}
	reg := prometheus.NewPedanticRegistry()
	s, err := newBlocksStoreReplicationSet(r, randomLoadBalancing, storegateway.NewNopDynamicReplication(ringCfg.ReplicationFactor), limits, grpcclient.Config{}, log.NewNopLogger(), reg)
	require.NoError(t, err)
	require.NoError(t, services.StartAndAwaitRunning(ctx, s))
	defer services.StopAndAwaitTerminated(ctx, s) //nolint:errcheck

	// Wait until the ring client has initialised the state.
	test.Poll(t, time.Second, true, func() interface{} {
		all, err := r.GetAllHealthy(storegateway.BlocksRead)
		return err == nil && len(all.Instances) > 0
	})

	// Request the same block multiple times and ensure the distribution of
	// requests across store-gateways is balanced.
	distribution := map[string]int{}

	for n := 0; n < numRuns; n++ {
		clients, err := s.GetClientsFor(userID, []*bucketindex.Block{block1}, nil)
		require.NoError(t, err)
		defer func() {
			// Close all clients to ensure no goroutines are leaked.
			for c := range clients {
				c.(io.Closer).Close() //nolint:errcheck
			}
		}()

		require.Len(t, clients, 1)

		for addr := range getStoreGatewayClientAddrs(clients) {
			distribution[addr]++
		}
	}

	assert.Len(t, distribution, numInstances)
	for addr, count := range distribution {
		// Ensure that the number of times each client is returned is above
		// the 80% of the perfect even distribution.
		assert.Greaterf(t, float64(count), (float64(numRuns)/float64(numInstances))*0.8, "store-gateway address: %s", addr)
	}
}

func getStoreGatewayClientAddrs(clients map[BlocksStoreClient][]ulid.ULID) map[string][]ulid.ULID {
	addrs := map[string][]ulid.ULID{}
	for c, blockIDs := range clients {
		addrs[c.RemoteAddress()] = blockIDs
	}
	return addrs
}
