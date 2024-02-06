package app

import (
	"context"
	"github.com/rs/zerolog/log"
	"github.com/sandwich-go/boost/xerror"
	"github.com/sandwich-go/boost/xpanic"
	"github.com/sandwich-go/redisson"
)

type Engine interface {
	// Clear 清理
	// pattern 匹配模式
	// count 单次扫描匹配返回的最大元素数量
	Clear(ctx context.Context, pattern string, count int64) error
}

type engine struct {
	cc redisson.ConfInterface
	redisson.Cmdable
}

// New 创建 Engine
func New(cc redisson.ConfInterface) (Engine, error) {
	var err error
	cc.ApplyOption(redisson.WithDevelopment(false))
	e := &engine{cc: cc, Cmdable: redisson.MustNewClient(cc)}
	e.Cmdable, err = redisson.Connect(cc)
	return e, err
}

// MustNew 创建 Engine
func MustNew(cc redisson.ConfInterface) Engine {
	e, err := New(cc)
	xpanic.WhenError(err)
	return e
}

// delete 删除
func (e *engine) delete(ctx context.Context, cmdable redisson.Cmdable, keys ...string) error {
	if !e.IsCluster() {
		return cmdable.Del(ctx, keys...).Err()
	}
	var err xerror.Array
	batch := cmdable.Pipeline()
	for _, key := range keys {
		err0 := batch.Put(ctx, redisson.CommandDel, []string{key})
		if err0 != nil {
			err.Push(err0)
		}
	}
	_, err0 := batch.Exec(ctx)
	if err0 != nil {
		err.Push(err0)
	}
	return err.Err()
}

// clear 清理
func (e *engine) clear(ctx context.Context, cmdable redisson.Cmdable, pattern string, count int64) error {
	var err error
	var cursor uint64
	var total int64
	for {
		var keys []string
		keys, cursor, err = cmdable.Scan(ctx, cursor, pattern, count).Result()
		if err != nil {
			break
		}
		if len(keys) > 0 {
			err = e.delete(ctx, cmdable, keys...)
			if err != nil {
				break
			}
			total += int64(len(keys))
			log.Info().Int("count", len(keys)).Msg("clear keys...")
		}
		if cursor == 0 {
			log.Info().Int64("total", total).Msg("clear keys completed")
			break
		}
	}
	return err
}

// Clear 清理
func (e *engine) Clear(ctx context.Context, pattern string, count int64) error {
	xpanic.WhenTrue(len(pattern) == 0, "pattern is empty")
	xpanic.WhenTrue(count <= 0, "count is invalid, need > 0")
	if e.IsCluster() {
		return e.Cmdable.ForEachNodes(ctx, func(_ctx context.Context, _cmdable redisson.Cmdable) error {
			return e.clear(_ctx, _cmdable, pattern, count)
		})
	}
	return e.clear(ctx, e.Cmdable, pattern, count)
}
