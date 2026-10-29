package app

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/rueidis"
	"github.com/sandwich-go/redisson"
)

// scripts/test-cluster.py 提供独立的 Docker 集群；普通 go test 不会连接配置文件中的 Redis。
func TestClusterClearIntegration(t *testing.T) {
	addr := os.Getenv("REDIS_TOOLS_CLUSTER_ADDR")
	if addr == "" {
		t.Skip("run python3 scripts/test-cluster.py to test a disposable Redis cluster")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, err := rueidis.NewClient(rueidis.ClientOption{InitAddress: []string{addr}, DisableCache: true})
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	newEngine := func(t *testing.T) *engine {
		t.Helper()
		e, err := New(redisson.NewConf(redisson.WithAddrs(addr), redisson.WithEnableCache(false)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = e.(*engine).Close() })
		return e.(*engine)
	}
	seed := func(t *testing.T) {
		t.Helper()
		var commands []rueidis.Completed
		for i := 0; i < 3000; i++ {
			commands = append(commands, admin.B().Set().Key(fmt.Sprintf("user:%d", i)).Value("value").Build())
		}
		for i := 0; i < 300; i++ {
			commands = append(commands, admin.B().Set().Key(fmt.Sprintf("keep:%d", i)).Value("value").Build())
		}
		for _, resp := range admin.DoMulti(ctx, commands...) {
			if err := resp.Error(); err != nil {
				t.Fatal(err)
			}
		}
	}
	assertKeys := func(t *testing.T, pattern string, want int) {
		t.Helper()
		keys := make(map[string]struct{})
		for _, node := range admin.Nodes() {
			info, err := node.Do(ctx, node.B().Info().Section("replication").Build()).ToString()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(info, "role:master") {
				continue
			}
			var cursor uint64
			for {
				scan, err := node.Do(ctx, node.B().Scan().Cursor(cursor).Match(pattern).Count(100).Build()).AsScanEntry()
				if err != nil {
					t.Fatal(err)
				}
				for _, key := range scan.Elements {
					keys[key] = struct{}{}
				}
				cursor = scan.Cursor
				if cursor == 0 {
					break
				}
			}
		}
		if len(keys) != want {
			t.Fatalf("remaining keys matching %q = %d, want %d", pattern, len(keys), want)
		}
	}
	t.Run("prefix preserves other keys", func(t *testing.T) {
		seed(t)
		if err := newEngine(t).Clear(ctx, ClearAllDB, "user:*", 17); err != nil {
			t.Fatal(err)
		}
		assertKeys(t, "user:*", 0)
		assertKeys(t, "keep:*", 300)
	})
	t.Run("full clear", func(t *testing.T) {
		seed(t)
		if err := newEngine(t).Clear(ctx, 0, "*", 17); err != nil {
			t.Fatal(err)
		}
		assertKeys(t, "*", 0)
	})
	t.Run("disabled flush falls back to scan", func(t *testing.T) {
		seed(t)
		for _, node := range admin.Nodes() {
			node := node
			if err := node.Do(ctx, node.B().Arbitrary("ACL", "SETUSER", "default", "-flushall", "-flushdb").Build()).Error(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := node.Do(ctx, node.B().Arbitrary("ACL", "SETUSER", "default", "+flushall", "+flushdb").Build()).Error(); err != nil {
					t.Error(err)
				}
			})
		}
		if err := newEngine(t).Clear(ctx, 0, "*", 17); err != nil {
			t.Fatal(err)
		}
		assertKeys(t, "*", 0)
	})
	t.Run("key migrates after destination scan with ASK redirect", func(t *testing.T) {
		const key = "user:{migration}:key"
		if err := admin.Do(ctx, admin.B().Set().Key(key).Value("value").Build()).Error(); err != nil {
			t.Fatal(err)
		}
		slot, err := admin.Do(ctx, admin.B().Arbitrary("CLUSTER", "KEYSLOT", key).Build()).ToInt64()
		if err != nil {
			t.Fatal(err)
		}
		primaries := make(map[string]rueidis.Client)
		var sourceID, targetID, targetPort string
		for endpoint, node := range admin.Nodes() {
			info, err := node.Do(ctx, node.B().Info().Section("replication").Build()).ToString()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(info, "role:master") {
				continue
			}
			id, err := node.Do(ctx, node.B().Arbitrary("CLUSTER", "MYID").Build()).ToString()
			if err != nil {
				t.Fatal(err)
			}
			primaries[id] = node
			if err := node.Do(ctx, node.B().Get().Key(key).Build()).Error(); err == nil {
				sourceID = id
			} else {
				targetID = id
				_, targetPort, err = net.SplitHostPort(endpoint)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		if sourceID == "" || targetID == "" {
			t.Fatal("could not identify source and destination primaries")
		}
		migrate := func() error {
			source, target := primaries[sourceID], primaries[targetID]
			commands := []struct {
				node rueidis.Client
				args []string
			}{
				{target, []string{"CLUSTER", "SETSLOT", fmt.Sprint(slot), "IMPORTING", sourceID}},
				{source, []string{"CLUSTER", "SETSLOT", fmt.Sprint(slot), "MIGRATING", targetID}},
				{source, []string{"MIGRATE", "127.0.0.1", targetPort, key, "0", "5000"}},
			}
			for _, command := range commands {
				if err := command.node.Do(ctx, command.node.B().Arbitrary(command.args...).Build()).Error(); err != nil {
					return err
				}
			}
			return nil
		}
		finishMigration := func() error {
			source, target := primaries[sourceID], primaries[targetID]
			for _, node := range append([]rueidis.Client{target, source}, clusterIntegrationOtherNodes(primaries, sourceID, targetID)...) {
				if err := node.Do(ctx, node.B().Arbitrary("CLUSTER", "SETSLOT", fmt.Sprint(slot), "NODE", targetID).Build()).Error(); err != nil {
					return err
				}
			}
			return nil
		}
		e := newEngine(t)
		connect := e.connect
		first := true
		migrated := make(chan struct{})
		var once sync.Once
		var finishOnce sync.Once
		var finishErr error
		afterDelete := func() error {
			// 保持 IMPORTING / MIGRATING，确保补清通过 ASK 重定向删除 key，随后完成迁槽。
			finishOnce.Do(func() { finishErr = finishMigration() })
			return finishErr
		}
		e.connect = func(conf redisson.ConfInterface) (redisson.Cmdable, error) {
			client, err := connect(conf)
			if err != nil {
				return client, err
			}
			if !first {
				return &clusterIntegrationClient{Cmdable: client, afterDelete: afterDelete}, nil
			}
			first = false
			return &clusterIntegrationClient{Cmdable: client, afterDelete: afterDelete, wrap: func(node redisson.Cmdable) (redisson.Cmdable, error) {
				state, err := readClusterNodeState(ctx, node)
				if err != nil {
					return nil, err
				}
				return &clusterIntegrationNode{Cmdable: node, scan: func(scanCtx context.Context, cursor uint64, pattern string, count int64) redisson.ScanCmd {
					if state.id == sourceID {
						select {
						case <-migrated:
						case <-scanCtx.Done():
							return clusterTestScan{err: scanCtx.Err()}
						}
					}
					resp := node.Scan(scanCtx, cursor, pattern, count)
					_, next, err := resp.Result()
					if state.id == targetID && next == 0 && err == nil {
						once.Do(func() {
							defer close(migrated)
							err = migrate()
						})
						if err != nil {
							return clusterTestScan{err: err}
						}
					}
					return resp
				}}, nil
			}}, nil
		}
		if err := e.Clear(ctx, 0, "user:*", 1); err != nil {
			t.Fatal(err)
		}
		select {
		case <-migrated:
		default:
			t.Fatal("migration hook did not execute")
		}
		assertKeys(t, "user:*", 0)
	})
	t.Run("continuing writes cannot report success", func(t *testing.T) {
		e := newEngine(t)
		connect := e.connect
		e.connect = func(conf redisson.ConfInterface) (redisson.Cmdable, error) {
			if err := admin.Do(ctx, admin.B().Set().Key("user:writer").Value("value").Build()).Error(); err != nil {
				return nil, err
			}
			return connect(conf)
		}
		if err := e.Clear(ctx, 0, "user:*", 17); err == nil {
			t.Fatal("Clear returned success while every round still found matching keys")
		}
		e.connect = connect
		if err := e.Clear(ctx, 0, "user:*", 17); err != nil {
			t.Fatal(err)
		}
		assertKeys(t, "*", 0)
	})
}

type clusterIntegrationClient struct {
	redisson.Cmdable
	wrap        func(redisson.Cmdable) (redisson.Cmdable, error)
	afterDelete func() error
}

func (c *clusterIntegrationClient) ForEachNodes(ctx context.Context, fn func(context.Context, redisson.Cmdable) error) error {
	return c.Cmdable.ForEachNodes(ctx, func(ctx context.Context, node redisson.Cmdable) error {
		if c.wrap == nil {
			return fn(ctx, node)
		}
		wrapped, err := c.wrap(node)
		if err != nil {
			return err
		}
		return fn(ctx, wrapped)
	})
}

func (c *clusterIntegrationClient) Pipeline() redisson.Pipeliner {
	return &clusterIntegrationPipeline{Pipeliner: c.Cmdable.Pipeline(), after: c.afterDelete}
}

type clusterIntegrationPipeline struct {
	redisson.Pipeliner
	after func() error
}

func (p *clusterIntegrationPipeline) Exec(ctx context.Context) ([]any, error) {
	result, err := p.Pipeliner.Exec(ctx)
	if err == nil && p.after != nil {
		err = p.after()
	}
	return result, err
}

type clusterIntegrationNode struct {
	redisson.Cmdable
	scan func(context.Context, uint64, string, int64) redisson.ScanCmd
}

func (n *clusterIntegrationNode) Scan(ctx context.Context, cursor uint64, pattern string, count int64) redisson.ScanCmd {
	return n.scan(ctx, cursor, pattern, count)
}

func clusterIntegrationOtherNodes(nodes map[string]rueidis.Client, ids ...string) []rueidis.Client {
	var result []rueidis.Client
	for id, node := range nodes {
		found := false
		for _, excluded := range ids {
			found = found || id == excluded
		}
		if !found {
			result = append(result, node)
		}
	}
	return result
}
