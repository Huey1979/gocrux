package bootstrap

import (
	errs "github.com/Huey1979/gocrux/errors"
	"github.com/Huey1979/gocrux/internal/config"
	"github.com/Huey1979/gocrux/internal/database/mongodb"
	"github.com/Huey1979/gocrux/internal/database/mysql"
	"github.com/Huey1979/gocrux/internal/database/redis"

	"github.com/sirupsen/logrus"
)

// InitMySQL 仅初始化 MySQL 连接（用于迁移前）
func InitMySQL() error {
	initLog()
	logrus.Info("正在连接 MySQL...")
	if err := mysql.Init(&config.Cfg.MySQL); err != nil {
		return errs.ErrInitMySQL(err)
	}
	return nil
}

// InitOther 初始化其他组件（MongoDB、Redis）
func InitOther() error {
	// 1. 初始化 MongoDB
	logrus.Info("正在连接 MongoDB...")
	if err := mongodb.Init(&config.Cfg.MongoDB); err != nil {
		return errs.ErrInitMongoDB(err)
	}
	logrus.Info("MongoDB 连接成功")

	// 2. 初始化 Redis
	logrus.Info("正在连接 Redis...")
	if err := redis.Init(&config.Cfg.Redis); err != nil {
		return errs.ErrInitRedis(err)
	}
	logrus.Info("Redis 连接成功")

	return nil
}

// Init 初始化所有组件
func Init() error {
	// 1. 初始化日志
	initLog()

	// 2. 初始化 MySQL
	if err := InitMySQL(); err != nil {
		return err
	}

	// 3. 初始化其他组件
	if err := InitOther(); err != nil {
		return err
	}

	return nil
}

// Migrate 执行数据库迁移（需外部注入模型列表）
func Migrate(models ...any) error {
	return mysql.Migrate(models...)
}

// initLog 初始化控制台日志
func initLog() {
	logrus.SetLevel(logrus.DebugLevel)
	logrus.SetFormatter(&logrus.TextFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
		FullTimestamp:   true,
		DisableColors:   false,
	})
}

// Close 关闭所有连接
func Close() error {
	logrus.Info("正在关闭数据库连接...")

	if err := mysql.Close(); err != nil {
		return errs.ErrCloseMySQL(err)
	}

	if err := mongodb.Close(); err != nil {
		return errs.ErrCloseMongoDB(err)
	}

	if err := redis.Close(); err != nil {
		return errs.ErrCloseRedis(err)
	}

	logrus.Info("所有数据库连接已关闭")
	return nil
}
