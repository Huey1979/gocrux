package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Huey1979/gocrux/internal/config"

	errs "github.com/Huey1979/gocrux/errors"
	applogger "github.com/Huey1979/gocrux/internal/logger"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// gdb 内部 GORM 实例，仅在 mysql 包内使用
var gdb *gorm.DB

// CtxDB 上下文数据库入口
// 所有数据库操作必须通过 WithCtx(ctx) 获取带上下文的 *gorm.DB，
// 这样 GORM Logger 才能从 context 中提取 log_id
type CtxDB struct{}

// DB 是外部唯一能访问的数据库入口
// 编译器强制：写 mysql.DB.Where(...) 直接编译报错，必须写 mysql.DB.WithCtx(ctx).Where(...)
var DB CtxDB

// WithCtx 获取带上下文的 GORM 实例
func (CtxDB) WithCtx(ctx context.Context) *gorm.DB {
	return gdb.WithContext(ctx)
}

// InternalDB 获取不带请求上下文的 GORM 实例
// 仅用于迁移、初始化等基础设施操作，业务代码请使用 WithCtx(ctx)
func (CtxDB) InternalDB() *gorm.DB {
	return gdb.WithContext(context.Background())
}

// ScanRows 暴露 GORM 的 ScanRows 工具方法（用于扫描已有 *sql.Rows）
func (CtxDB) ScanRows(rows *sql.Rows, dest interface{}) error {
	return gdb.ScanRows(rows, dest)
}

// Init 初始化 MySQL 连接
func Init(cfg *config.MySQLConfig) error {
	var err error

	// 配置 GORM（自定义 Logger 将 SQL 写入 business 日志）
	gormCfg := &gorm.Config{
		Logger: applogger.NewGormLogger(200 * time.Millisecond), // 超过 200ms 视为慢查询
		NowFunc: func() time.Time {
			return time.Now().Local()
		},
	}

	// 连接 MySQL
	gdb, err = gorm.Open(mysql.Open(cfg.DSN()), gormCfg)
	if err != nil {
		return errs.ErrMySQLConnect(err)
	}

	// 配置连接池
	sqlDB, err := gdb.DB()
	if err != nil {
		return errs.ErrDBInstance(err)
	}

	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(time.Duration(cfg.MaxLifeTime) * time.Second)

	// 确保数据库字符集为 utf8mb4
	if err := ensureCharset(cfg); err != nil {
		return errs.ErrDBCharset(err)
	}

	return nil
}

// ensureCharset 确保数据库字符集为 utf8mb4
func ensureCharset(cfg *config.MySQLConfig) error {
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}

	// 修改数据库字符集
	_, err = sqlDB.Exec(fmt.Sprintf("ALTER DATABASE `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", cfg.Database))
	if err != nil {
		return errs.ErrDBModifyCharset(err)
	}

	return nil
}

// Close 关闭 MySQL 连接
func Close() error {
	if gdb != nil {
		sqlDB, err := gdb.DB()
		if err != nil {
			return err
		}
		return sqlDB.Close()
	}
	return nil
}
