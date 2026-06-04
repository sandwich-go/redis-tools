package app

import (
	"context"
	"fmt"
	"github.com/rs/zerolog/log"
	"github.com/sandwich-go/boost/xerror"
	"github.com/sandwich-go/boost/xpanic"
	"github.com/sandwich-go/redisson"
	"strconv"
	"strings"
)

// ClearAllDB 表示清理所有 db
const ClearAllDB = -1

type Engine interface {
	// Clear 清理
	// db 指定清理的 db，传入 ClearAllDB(-1) 表示清理所有 db；集群模式下该参数被忽略
	// pattern 匹配模式
	// count 单次扫描匹配返回的最大元素数量
	Clear(ctx context.Context, db int, pattern string, count int64) error
}

type engine struct {
	cc redisson.ConfInterface
	redisson.Cmdable
	db int // 主连接绑定的 db（配置 db），用于判断能否复用主连接
}

// New 创建 Engine
func New(cc redisson.ConfInterface) (Engine, error) {
	var err error
	cc.ApplyOption(redisson.WithDevelopment(false))
	e := &engine{cc: cc, db: cc.GetDB()}
	e.Cmdable, err = redisson.Connect(cc)
	if err == nil {
		log.Info().Any("config", e.Cmdable.Options().(*redisson.Conf)).Msg("connect redis")
	} else {
		log.Error().Any("config", cc.(*redisson.Conf)).Err(err).Msg("connect redis")
	}
	return e, err
}

// MustNew 创建 Engine
func MustNew(cc redisson.ConfInterface) Engine {
	e, err := New(cc)
	xpanic.WhenError(err)
	return e
}

// delete 删除
func (e *engine) delete(ctx context.Context, cmdable, nodeCmdable redisson.Cmdable, keys ...string) error {
	if !e.IsCluster() {
		return nodeCmdable.Del(ctx, keys...).Err()
	}
	// 先尝试
	batch := cmdable.Pipeline()
	for _, key := range keys {
		redisson.CommandDel.P(batch).Cmd(key)
	}
	_, err0 := batch.Exec(ctx)
	if err0 == nil {
		return nil
	}

	var moveKey []string
	var err xerror.Array
	for _, key := range keys {
		err0 := nodeCmdable.Del(ctx, key).Err()
		if err0 != nil {
			if strings.Contains(err0.Error(), "MOVE") {
				moveKey = append(moveKey, key)
			} else {
				err.Push(err0)
			}
		}
	}
	if err1 := err.Err(); err1 != nil {
		return err1
	}
	if len(moveKey) > 0 {
		log.Warn().Strs("moveKey", moveKey).Msg("have move keys...")
		batch = cmdable.Pipeline()
		for _, key := range moveKey {
			redisson.CommandDel.P(batch).Cmd(key)
		}
		_, err0 := batch.Exec(ctx)
		if err0 != nil {
			err.Push(err0)
		}
		return err.Err()
	}
	return nil
}

// clear 清理
func (e *engine) clear(ctx context.Context, cmdable, nodeCmdable redisson.Cmdable, pattern string, count int64) error {
	var err error
	var cursor uint64
	var total int64
	for {
		var keys []string
		keys, cursor, err = nodeCmdable.Scan(ctx, cursor, pattern, count).Result()
		if err != nil {
			break
		}
		if len(keys) > 0 {
			err = e.delete(ctx, cmdable, nodeCmdable, keys...)
			if err != nil {
				break
			}
			total += int64(len(keys))
			log.Info().Int("count", len(keys)).Strs("addr", e.cc.GetAddrs()).Msg("clear keys...")
		}
		if cursor == 0 {
			log.Info().Int64("total", total).Strs("addr", e.cc.GetAddrs()).Msg("clear keys completed")
			break
		}
	}
	return err
}

// databaseCount 获取实例配置的 db 总数
func (e *engine) databaseCount(ctx context.Context) (int, error) {
	res, err := e.ConfigGet(ctx, "databases").Result()
	if err != nil {
		return 0, err
	}
	v, ok := res["databases"]
	if !ok {
		return 0, fmt.Errorf("config databases not found")
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("parse databases count %q: %w", v, err)
	}
	return n, nil
}

// clearOnDB 在指定 db 上清理
// redisson 在连接时绑定 db，运行时无法 SELECT，故为目标 db 单独建立连接
func (e *engine) clearOnDB(ctx context.Context, db int, pattern string, count int64) error {
	// 目标 db 即主连接所在 db，复用主连接，与未指定 db 时行为完全一致
	if db == e.db {
		log.Info().Int("db", db).Strs("addr", e.cc.GetAddrs()).Msg("start clear db")
		return e.clear(ctx, e.Cmdable, e.Cmdable, pattern, count)
	}
	e.cc.ApplyOption(redisson.WithDB(db))
	c, err := redisson.Connect(e.cc)
	if err != nil {
		log.Error().Int("db", db).Strs("addr", e.cc.GetAddrs()).Err(err).Msg("connect redis failed")
		return err
	}
	defer func() { _ = c.Close() }()
	log.Info().Int("db", db).Strs("addr", e.cc.GetAddrs()).Msg("start clear db")
	return e.clear(ctx, c, c, pattern, count)
}

// Clear 清理
func (e *engine) Clear(ctx context.Context, db int, pattern string, count int64) error {
	xpanic.WhenTrue(len(pattern) == 0, "pattern is empty")
	xpanic.WhenTrue(count <= 0, "count is invalid, need > 0")
	if e.IsCluster() {
		if db > 0 {
			log.Warn().Int("db", db).Msg("cluster mode only supports db 0, ignore db flag")
		}
		return e.Cmdable.ForEachNodes(ctx, func(_ctx context.Context, _cmdable redisson.Cmdable) error {
			return e.clear(_ctx, e.Cmdable, _cmdable, pattern, count)
		})
	}
	var dbs []int
	if db == ClearAllDB {
		n, err := e.databaseCount(ctx)
		if err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			dbs = append(dbs, i)
		}
		log.Info().Int("databases", n).Msg("clear all db")
	} else {
		dbs = []int{db}
	}
	var errs xerror.Array
	for _, d := range dbs {
		if err := e.clearOnDB(ctx, d, pattern, count); err != nil {
			errs.Push(err)
		}
	}
	return errs.Err()
}
