package app

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/sandwich-go/redisson"
)

func TestIsFullPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    bool
	}{
		{name: "empty", pattern: "", want: true},
		{name: "blank", pattern: " \t\n", want: true},
		{name: "star", pattern: "*", want: true},
		{name: "star with spaces", pattern: " * ", want: true},
		{name: "prefix glob", pattern: "user*", want: false},
		{name: "contains star", pattern: "user:*", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isFullPattern(tt.pattern); got != tt.want {
				t.Fatalf("isFullPattern(%q) = %v, want %v", tt.pattern, got, tt.want)
			}
		})
	}
}

func TestClearAllFallsBackToScanWhenFlushCommandsAreDisabled(t *testing.T) {
	redis := miniredis.RunT(t)
	if err := redis.DB(0).Set("db0-key", "value"); err != nil {
		t.Fatal(err)
	}
	if err := redis.DB(1).Set("db1-key", "value"); err != nil {
		t.Fatal(err)
	}

	var flushCommandCount int32
	redis.Server().SetPreHook(func(peer *server.Peer, command string, _ ...string) bool {
		switch command {
		case "INFO":
			peer.Block(func(writer *server.Writer) {
				writer.WriteBulk("# Server\r\nredis_version:7.0.0\r\n# Cluster\r\ncluster_enabled:0\r\n")
			})
			return true
		case "ROLE":
			peer.Block(func(writer *server.Writer) {
				writer.WriteRaw("*3\r\n$6\r\nmaster\r\n:0\r\n*0\r\n")
			})
			return true
		case "FLUSHALL", "FLUSHDB":
			atomic.AddInt32(&flushCommandCount, 1)
			peer.WriteError("command is disabled")
			return true
		default:
			return false
		}
	})

	engine, err := New(redisson.NewConf(
		redisson.WithAddrs(redis.Addr()),
		redisson.WithDB(0),
	))
	if err != nil {
		t.Fatal(err)
	}
	if closable, ok := engine.(interface{ Close() error }); ok {
		defer func() { _ = closable.Close() }()
	}

	if err := engine.Clear(context.Background(), ClearAllDB, "*", 1); err != nil {
		t.Fatalf("clear all redis db: %v", err)
	}
	if got := redis.DB(0).Keys(); len(got) != 0 {
		t.Fatalf("db 0 keys = %v, want empty", got)
	}
	if got := redis.DB(1).Keys(); len(got) != 0 {
		t.Fatalf("db 1 keys = %v, want empty", got)
	}
	if got := atomic.LoadInt32(&flushCommandCount); got != 1 {
		t.Fatalf("flush command count = %d, want 1", got)
	}
}

func TestForEachNodeConcurrentlyRecoversPanic(t *testing.T) {
	e, err := New(redisson.NewConf(redisson.WithT(t)))
	if err != nil {
		t.Fatal(err)
	}
	if closable, ok := e.(interface{ Close() error }); ok {
		defer func() { _ = closable.Close() }()
	}

	err = e.(*engine).forEachNodeConcurrently(context.Background(), func(context.Context, redisson.Cmdable) error {
		panic("node failed")
	})
	if err == nil {
		t.Fatal("forEachNodeConcurrently() error = nil, want panic error")
	}
	if got := err.Error(); !strings.Contains(got, "cluster node cleanup task panic recovered") || !strings.Contains(got, "node failed") {
		t.Fatalf("forEachNodeConcurrently() error = %q, want panic details", got)
	}
}
