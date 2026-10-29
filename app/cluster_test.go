package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sandwich-go/redisson"
)

type clusterTestString struct {
	redisson.StringCmd
	value string
}

func (c clusterTestString) Result() (string, error) { return c.value, nil }

type clusterTestScan struct {
	keys   []string
	cursor uint64
	err    error
}

func (c clusterTestScan) Err() error                        { return c.err }
func (c clusterTestScan) Val() ([]string, uint64)           { return c.keys, c.cursor }
func (c clusterTestScan) Result() ([]string, uint64, error) { return c.keys, c.cursor, c.err }

type clusterTestNode struct {
	redisson.Cmdable
	topology string
	scan     func(context.Context, uint64, string, int64) redisson.ScanCmd
}

func (n *clusterTestNode) ClusterNodes(context.Context) redisson.StringCmd {
	return clusterTestString{value: n.topology}
}

func (n *clusterTestNode) Scan(ctx context.Context, cursor uint64, pattern string, count int64) redisson.ScanCmd {
	if n.scan != nil {
		return n.scan(ctx, cursor, pattern, count)
	}
	return n.Cmdable.Scan(ctx, cursor, pattern, count)
}

type clusterTestClient struct {
	redisson.Cmdable
	nodes   []redisson.Cmdable
	onClose func()
}

func (*clusterTestClient) IsCluster() bool { return true }
func (c *clusterTestClient) ForEachNodes(ctx context.Context, fn func(context.Context, redisson.Cmdable) error) error {
	for _, node := range c.nodes {
		if err := fn(ctx, node); err != nil {
			return err
		}
	}
	return nil
}
func (c *clusterTestClient) Close() error {
	if c.onClose != nil {
		c.onClose()
	}
	return nil
}

func clusterTestTopology(self string, epoch int, migration string) string {
	var lines []string
	for i, id := range []string{"a", "b", "replica"} {
		flags, primary, slots := "master", "-", "0-8191"
		if id == "b" {
			slots = "8192-16383"
		}
		if id == "replica" {
			flags, primary, slots = "slave", "a", ""
		}
		if id == self {
			flags = "myself," + flags
			slots += " " + migration
		}
		lines = append(lines, fmt.Sprintf("%s 127.0.0.1:%d@%d %s %s 0 0 %d connected %s", id, 7000+i, 17000+i, flags, primary, epoch, slots))
	}
	return strings.Join(lines, "\n")
}

func clusterTestBase(t *testing.T) redisson.Cmdable {
	t.Helper()
	c, err := redisson.Connect(redisson.NewConf(redisson.WithT(t), redisson.WithEnableCache(false)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func clusterTestEngine(connect func(redisson.ConfInterface) (redisson.Cmdable, error)) *engine {
	return &engine{cc: redisson.NewConf(), Cmdable: &clusterTestClient{}, connect: connect}
}

func emptyClusterTestScan(context.Context, uint64, string, int64) redisson.ScanCmd {
	return clusterTestScan{}
}

func TestReadClusterNodeStateRESP3(t *testing.T) {
	for _, self := range []string{"a", "b", "replica"} {
		t.Run(self, func(t *testing.T) {
			node := &clusterTestNode{topology: "txt:" + clusterTestTopology(self, 1, "")}
			state, err := readClusterNodeState(context.Background(), node)
			if err != nil {
				t.Fatal(err)
			}
			if state.id != self || strings.Join(state.primaries, ",") != "a,b" {
				t.Fatalf("incorrect node IDs from RESP3 response: %+v", state)
			}
		})
	}
}

func TestClusterClearCatchesKeyMovedToAlreadyScannedNode(t *testing.T) {
	base := clusterTestBase(t)
	ctx := context.Background()
	if err := base.Set(ctx, "user:move", "value", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := base.Set(ctx, "keep", "value", 0).Err(); err != nil {
		t.Fatal(err)
	}
	var owner int32 // 0: b, 1: a
	rounds, closed := 0, 0
	e := clusterTestEngine(func(redisson.ConfInterface) (redisson.Cmdable, error) {
		rounds++
		firstRound := rounds == 1
		targetScanned := make(chan struct{})
		a := &clusterTestNode{Cmdable: base, topology: clusterTestTopology("a", 1, "")}
		b := &clusterTestNode{Cmdable: base, topology: clusterTestTopology("b", 1, "")}
		a.scan = func(ctx context.Context, cursor uint64, pattern string, count int64) redisson.ScanCmd {
			if firstRound {
				close(targetScanned)
				return clusterTestScan{}
			}
			if atomic.LoadInt32(&owner) == 1 {
				return base.Scan(ctx, cursor, pattern, count)
			}
			return clusterTestScan{}
		}
		b.scan = func(context.Context, uint64, string, int64) redisson.ScanCmd {
			if firstRound {
				<-targetScanned
				atomic.StoreInt32(&owner, 1)
			}
			return clusterTestScan{}
		}
		return &clusterTestClient{Cmdable: base, nodes: []redisson.Cmdable{a, b}, onClose: func() { closed++ }}, nil
	})
	if err := e.Clear(ctx, 0, "user:*", 1); err != nil {
		t.Fatal(err)
	}
	if n := base.Exists(ctx, "user:move").Val(); n != 0 {
		t.Fatal("matching key moved to an already-scanned node was not deleted")
	}
	if n := base.Exists(ctx, "keep").Val(); n != 1 {
		t.Fatal("unmatched key was deleted")
	}
	if closed != rounds {
		t.Fatalf("closed %d of %d refreshed clients", closed, rounds)
	}
}

func TestClusterClearRefreshesMissingPrimary(t *testing.T) {
	base := clusterTestBase(t)
	ctx := context.Background()
	if err := base.Set(ctx, "user:new-primary", "value", 0).Err(); err != nil {
		t.Fatal(err)
	}
	rounds := 0
	e := clusterTestEngine(func(redisson.ConfInterface) (redisson.Cmdable, error) {
		rounds++
		nodes := []redisson.Cmdable{&clusterTestNode{topology: clusterTestTopology("a", 1, ""), scan: emptyClusterTestScan}}
		if rounds > 1 {
			nodes = append(nodes, &clusterTestNode{Cmdable: base, topology: clusterTestTopology("b", 1, "")})
		}
		return &clusterTestClient{Cmdable: base, nodes: nodes}, nil
	})
	if err := e.Clear(ctx, 0, "user:*", 1); err != nil {
		t.Fatal(err)
	}
	if n := base.Exists(ctx, "user:new-primary").Val(); n != 0 {
		t.Fatal("key on newly discovered primary remains")
	}
}

func TestClusterClearRefusesUnverifiedSuccess(t *testing.T) {
	for _, scenario := range []string{"migration", "topology changes", "node error", "new writes"} {
		t.Run(scenario, func(t *testing.T) {
			var base redisson.Cmdable
			if scenario == "new writes" {
				base = clusterTestBase(t)
			}
			rounds, closed := 0, 0
			e := clusterTestEngine(func(redisson.ConfInterface) (redisson.Cmdable, error) {
				rounds++
				epoch, migration := 1, ""
				if scenario == "migration" {
					migration = "[42->-b]"
				}
				if scenario == "topology changes" {
					epoch = rounds
				}
				a := &clusterTestNode{Cmdable: base, topology: clusterTestTopology("a", epoch, migration), scan: emptyClusterTestScan}
				b := &clusterTestNode{topology: clusterTestTopology("b", epoch, ""), scan: emptyClusterTestScan}
				switch scenario {
				case "node error":
					b.scan = func(context.Context, uint64, string, int64) redisson.ScanCmd {
						return clusterTestScan{err: errors.New("node unavailable")}
					}
				case "new writes":
					if err := base.Set(context.Background(), "user:writer", "value", 0).Err(); err != nil {
						return nil, err
					}
					a.scan = nil
				}
				return &clusterTestClient{Cmdable: base, nodes: []redisson.Cmdable{a, b}, onClose: func() { closed++ }}, nil
			})
			if err := e.Clear(context.Background(), 0, "user:*", 1); err == nil {
				t.Fatal("Clear returned success without verifying an empty stable cluster")
			}
			if rounds != clusterClearMaxRounds || closed != rounds {
				t.Fatalf("rounds = %d, closed = %d; want %d bounded rounds with clients closed", rounds, closed, clusterClearMaxRounds)
			}
		})
	}
}

func TestClusterClearSkipsReplicas(t *testing.T) {
	var replicaScans int32
	e := clusterTestEngine(func(redisson.ConfInterface) (redisson.Cmdable, error) {
		a := &clusterTestNode{topology: clusterTestTopology("a", 1, ""), scan: emptyClusterTestScan}
		b := &clusterTestNode{topology: clusterTestTopology("b", 1, ""), scan: emptyClusterTestScan}
		replica := &clusterTestNode{topology: clusterTestTopology("replica", 1, ""), scan: func(context.Context, uint64, string, int64) redisson.ScanCmd {
			atomic.AddInt32(&replicaScans, 1)
			return clusterTestScan{keys: []string{"user:stale"}}
		}}
		return &clusterTestClient{nodes: []redisson.Cmdable{a, b, replica}}, nil
	})
	if err := e.Clear(context.Background(), 0, "user:*", 1); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&replicaScans); got != 0 {
		t.Fatalf("scanned replica %d times", got)
	}
}

func TestClusterClearCancelsRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rounds := 0
	e := clusterTestEngine(func(redisson.ConfInterface) (redisson.Cmdable, error) {
		rounds++
		time.AfterFunc(20*time.Millisecond, cancel)
		return nil, errors.New("temporarily unavailable")
	})
	if err := e.Clear(ctx, 0, "user:*", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Clear error = %v, want context.Canceled", err)
	}
	if rounds != 1 {
		t.Fatalf("started %d rounds after cancellation", rounds)
	}
}

func TestClearContinuesAfterEmptyScanPage(t *testing.T) {
	base := clusterTestBase(t)
	ctx := context.Background()
	if err := base.Set(ctx, "user:last-page", "value", 0).Err(); err != nil {
		t.Fatal(err)
	}
	node := &clusterTestNode{scan: func(_ context.Context, cursor uint64, _ string, _ int64) redisson.ScanCmd {
		if cursor == 0 {
			return clusterTestScan{cursor: 7}
		}
		return clusterTestScan{keys: []string{"user:last-page"}}
	}}
	e := clusterTestEngine(nil)
	if _, err := e.clear(ctx, base, node, "user:*", 1); err != nil {
		t.Fatal(err)
	}
	if n := base.Exists(ctx, "user:last-page").Val(); n != 0 {
		t.Fatal("key after an empty page was not deleted")
	}
}
