package redis

import (
	"context"

	"github.com/Huey1979/gocrux/internal/config"

	errs "github.com/Huey1979/gocrux/errors"
	"github.com/go-redis/redis/v8"
)

var Client *redis.Client
var Ctx = context.Background()

// Init 初始化 Redis 连接
func Init(cfg *config.RedisConfig) error {
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr(),
		Password: cfg.Password,
		DB:       cfg.DB,
		PoolSize: cfg.PoolSize,
	})

	// 测试连接
	if err := client.Ping(Ctx).Err(); err != nil {
		return errs.ErrRedisConnect(err)
	}

	Client = client
	return nil
}

// Close 关闭 Redis 连接
func Close() error {
	if Client != nil {
		return Client.Close()
	}
	return nil
}
