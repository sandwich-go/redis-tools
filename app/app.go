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

// Delete 删除
func (e *engine) Delete(ctx context.Context, keys ...string) error {
	if !e.IsCluster() {
		return e.Cmdable.Del(ctx, keys...).Err()
	}
	var err xerror.Array
	batch := e.Cmdable.Pipeline()
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

// Clear 清理
func (e *engine) Clear(ctx context.Context, pattern string, count int64) error {
	xpanic.WhenTrue(len(pattern) == 0, "pattern is empty")
	xpanic.WhenTrue(count <= 0, "count is invalid, need > 0")
	var err error
	var cursor uint64
	var total int64
	for {
		var keys []string
		keys, cursor, err = e.Cmdable.Scan(ctx, cursor, pattern, count).Result()
		if err != nil {
			break
		}
		if len(keys) > 0 {
			err = e.Delete(ctx, keys...)
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
