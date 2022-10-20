package itests

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/filecoin-project/lotus/itests/kit"
)

func TestMirConsensus(t *testing.T) {
	t.Run("mir", func(t *testing.T) {
		runMirConsensusTests(t, kit.ThroughRPC())
	})
}

func runMirConsensusTests(t *testing.T, opts ...interface{}) {
	ts := eudicoConsensusSuite{opts: opts}

	t.Run("testMirMiningOneNode", ts.testMirMiningOneNode)
	t.Run("testMirMiningTwoNodes", ts.testMirMiningTwoNodes)
	t.Run("testMirMiningFourNodes", ts.testMirMiningFourNodes)
}

type eudicoConsensusSuite struct {
	opts []interface{}
}

func (ts *eudicoConsensusSuite) testMirMiningOneNode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		t.Logf("[*] defer: cancelling %s context", t.Name())
		cancel()
	}()

	full, miner, ens := kit.EnsembleMinimalMir(t, ts.opts...)
	ens.BeginMirMining(ctx, miner)

	err := kit.SubnetHeightCheckForBlocks(ctx, 10, full)
	require.NoError(t, err)
}

func (ts *eudicoConsensusSuite) testMirMiningTwoNodes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		t.Logf("[*] defer: cancelling %s context", t.Name())
		cancel()
	}()

	n1, n2, m1, m2, ens := kit.EnsembleTwoMirNodes(t, ts.opts...)

	// Fail if genesis blocks are different
	gen1, err := n1.ChainGetGenesis(ctx)
	require.NoError(t, err)
	gen2, err := n2.ChainGetGenesis(ctx)
	require.NoError(t, err)
	require.Equal(t, gen1.String(), gen2.String())

	// Fail if nodes have peers
	p, err := n1.NetPeers(ctx)
	require.NoError(t, err)
	require.Empty(t, p, "node one has peers")

	p, err = n2.NetPeers(ctx)
	require.NoError(t, err)
	require.Empty(t, p, "node two has peers")

	ens.Connect(n1, n2)

	ens.BeginMirMining(ctx, m1, m2)

	err = kit.SubnetHeightCheckForBlocks(ctx, 10, n1)
	require.NoError(t, err)

	err = kit.SubnetHeightCheckForBlocks(ctx, 10, n2)
	require.NoError(t, err)
}

func (ts *eudicoConsensusSuite) testMirMiningFourNodes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		t.Logf("[*] defer: cancelling %s context", t.Name())
		cancel()
	}()

	nodes, miners, ens := kit.EnsembleMirNodes(t, 4, ts.opts...)
	require.Equal(t, 4, len(nodes))
	require.Equal(t, 4, len(miners))

	for i, n := range nodes {
		p, err := n.NetPeers(ctx)
		require.NoError(t, err)
		require.Empty(t, p, "node has peers", "nodeID", i)
	}

	ens.Connect(nodes[0], nodes[1], nodes[2], nodes[3])

	ens.BeginMirMining(ctx, miners...)

	for _, n := range nodes {
		err := kit.SubnetHeightCheckForBlocks(ctx, 10, n)
		require.NoError(t, err)
	}

}
