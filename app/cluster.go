package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sandwich-go/redisson"
)

const (
	clusterClearMaxRounds   = 10
	clusterClearEmptyRounds = 2
	clusterClearRetryDelay  = 200 * time.Millisecond
)

// clusterNodeState 只使用 CLUSTER NODES 中自身的状态。迁槽标记只出现在自身条目上。
type clusterNodeState struct {
	id        string
	primary   bool
	migrating bool
	signature string
	primaries []string
}

func readClusterNodeState(ctx context.Context, node redisson.Cmdable) (clusterNodeState, error) {
	var state clusterNodeState
	res, err := node.ClusterNodes(ctx).Result()
	if err != nil {
		return state, fmt.Errorf("read cluster node state: %w", err)
	}
	// 当前版本的 rueidis 会保留 RESP3 verbatim string 的 txt: 前缀。
	res = strings.TrimPrefix(res, "txt:")
	for _, line := range strings.Split(strings.TrimSpace(res), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			return state, fmt.Errorf("invalid CLUSTER NODES entry %q", line)
		}
		flags := "," + fields[2] + ","
		primary := strings.Contains(flags, ",master,")
		if primary {
			state.primaries = append(state.primaries, fields[0])
		}
		if !strings.Contains(flags, ",myself,") {
			continue
		}
		state.id, state.primary = fields[0], primary
		slots := append([]string(nil), fields[8:]...)
		sort.Strings(slots)
		state.signature = strings.Join(append([]string{fields[0], fields[1], fields[2], fields[6]}, slots...), " ")
		for _, slot := range slots {
			if strings.HasPrefix(slot, "[") {
				state.migrating = true
			}
		}
	}
	if state.id == "" {
		return state, fmt.Errorf("CLUSTER NODES response has no myself entry")
	}
	return state, nil
}

// clusterClearRound 使用新连接重新发现拓扑，避免依赖只在重定向时更新的节点缓存。
func (e *engine) clusterClearRound(ctx context.Context, pattern string, count int64, flush bool) (int64, string, error) {
	client, err := e.connect(e.cc)
	if err != nil {
		return 0, "", fmt.Errorf("refresh cluster connection: %w", err)
	}
	defer func() { _ = client.Close() }()
	if !client.IsCluster() {
		return 0, "", fmt.Errorf("refreshed redis connection is not a cluster")
	}
	var (
		mu        sync.Mutex
		total     int64
		states    = make(map[string]string)
		primaries = make(map[string]struct{})
	)
	err = e.forEachNodeConcurrently(ctx, client, func(nodeCtx context.Context, node redisson.Cmdable) error {
		before, err := readClusterNodeState(nodeCtx, node)
		if err != nil {
			return err
		}
		mu.Lock()
		for _, id := range before.primaries {
			primaries[id] = struct{}{}
		}
		mu.Unlock()
		// 副本通过复制跟随主节点清理，不扫描滞后的副本或向副本发送删除命令。
		if !before.primary {
			return nil
		}
		if flush {
			if err := e.flushAll(nodeCtx, node); err != nil {
				log.Warn().Str("node", before.id).Err(err).Msg("flush all failed, fallback to scan")
			}
		}
		matched, err := e.clear(nodeCtx, client, node, pattern, count)
		mu.Lock()
		total += matched
		mu.Unlock()
		if err != nil {
			return fmt.Errorf("clear cluster node %s: %w", before.id, err)
		}
		after, err := readClusterNodeState(nodeCtx, node)
		if err != nil {
			return err
		}
		if before.migrating || after.migrating {
			return fmt.Errorf("cluster node %s has a slot migration in progress", before.id)
		}
		if !after.primary || before.signature != after.signature {
			return fmt.Errorf("cluster node %s changed topology during cleanup", before.id)
		}
		mu.Lock()
		states[after.id] = after.signature
		for _, id := range after.primaries {
			primaries[id] = struct{}{}
		}
		mu.Unlock()
		return nil
	})
	if err != nil {
		return total, "", err
	}
	if len(states) == 0 {
		return total, "", fmt.Errorf("no primary nodes were scanned")
	}
	for id := range primaries {
		if _, ok := states[id]; !ok {
			return total, "", fmt.Errorf("cluster primary %s was not scanned; topology changed or node is unavailable", id)
		}
	}
	signatures := make([]string, 0, len(states))
	for _, signature := range states {
		signatures = append(signatures, signature)
	}
	sort.Strings(signatures)
	return total, strings.Join(signatures, "\n"), nil
}

func (e *engine) clearCluster(ctx context.Context, pattern string, count int64, fullPattern bool) error {
	var (
		emptyRounds int
		previous    string
		matched     int64
		lastErr     error
	)
	for round := 1; round <= clusterClearMaxRounds; round++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var topology string
		matched, topology, lastErr = e.clusterClearRound(ctx, pattern, count, fullPattern && round == 1)
		if err := ctx.Err(); err != nil {
			return err
		}
		if lastErr != nil {
			emptyRounds = 0
			previous = ""
			log.Warn().Int("round", round).Err(lastErr).Msg("cluster cleanup round failed")
		} else {
			if matched == 0 {
				if topology != previous {
					emptyRounds = 0
				}
				emptyRounds++
			} else {
				emptyRounds = 0
			}
			previous = topology
			log.Info().Int("round", round).Int64("matched", matched).Int("empty_rounds", emptyRounds).Msg("cluster cleanup round completed")
			if emptyRounds >= clusterClearEmptyRounds {
				return nil
			}
		}
		if round < clusterClearMaxRounds {
			timer := time.NewTimer(clusterClearRetryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	if lastErr != nil {
		return fmt.Errorf("cluster cleanup could not be verified after %d rounds: %w", clusterClearMaxRounds, lastErr)
	}
	return fmt.Errorf("cluster cleanup did not stay empty for %d consecutive rounds after %d rounds (last round matched %d keys); pause matching writes and slot migrations before retrying", clusterClearEmptyRounds, clusterClearMaxRounds, matched)
}
