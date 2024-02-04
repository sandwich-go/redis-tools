package config

import (
	"github.com/sandwich-go/redis-tools/pkg/util"
	"github.com/sandwich-go/redisson"
	"sync"
)

var (
	once sync.Once
	c    redisson.ConfInterface
)

func Get() redisson.ConfInterface { return c }

func MustInitialize(files ...string) {
	once.Do(func() {
		c = mustLoadConfig(files...)
		correctConfig(c)
		printConfig(c)
	})
}

// mustLoadConfig 加载配置
// 如果初始化失败，则 panic
func mustLoadConfig(files ...string) redisson.ConfInterface {
	cc := redisson.NewConf()
	util.MustInitialize(cc, files...)
	return cc
}

// correctSettings 矫正配置
func correctConfig(redisson.ConfInterface) {}

// printConfig 打印配置
func printConfig(cc redisson.ConfInterface) {
	util.PrintYamlConfig(cc, true)
}
