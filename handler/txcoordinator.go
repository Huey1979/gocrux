package handler

import (
	"context"

	"github.com/Huey1979/gocrux/common"

	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/mongo"
	"gorm.io/gorm"
)

// ============================================================
// TxCoordinator — 事务编排器
//
// 封装 gorm.DB（MySQL）和 mongo.Database（MongoDB），
// Handler 层不直接接触底层连接。
//
// MySQL:
//
//	tc := NewTxCoordinator(db, nil)
//	err := tc.Run(ctx, func(txCtx context.Context) error { ... })
//
// MongoDB:
//
//	tc := NewTxCoordinator(nil, mongoDB)
//	err := tc.Run(ctx, func(txCtx context.Context) error { ... })
//
// 双库：
//
//	tc := NewTxCoordinator(db, mongoDB)
//	err := tc.RunMySQL(ctx, func(txCtx context.Context) error { ... })
//	err := tc.RunMongo(ctx, func(txCtx context.Context) error { ... })
// ============================================================

type TxCoordinator struct {
	db      *gorm.DB
	mongoDB *mongo.Database

	// retryOnPKConflict 是否在主键冲突时整树回滚并重试（设计文档 §7.2 B8/B10）。
	//
	// **必须显式声明**（不做运行时探测）：运行时探测要么启动时试一次（结果缓存）、
	// 要么每次请求都试（浪费），且行为不可预期。显式声明让「本部署是否支持
	// 事务」成为一份可读的配置事实。
	//
	// 只有真正支持事务的部署（MySQL / Mongo 副本集）才能开启 —— 无事务时
	// 重试会留下「半棵树」，比直接报错更糟（§7.3）。
	retryOnPKConflict bool

	// maxPKConflictRetries 冲突重试次数（默认 1，可配 0 表示不重试）。
	maxPKConflictRetries int
}

// NewTxCoordinator 创建事务编排器。db / mongoDB 可各自为 nil。
func NewTxCoordinator(db *gorm.DB, mongoDB *mongo.Database) *TxCoordinator {
	return &TxCoordinator{db: db, mongoDB: mongoDB, maxPKConflictRetries: 1}
}

// SetRetryOnPKConflict 显式声明「本部署支持事务，主键冲突时整树回滚并可重试」。
//
// 语义（设计文档 §7.2）：
//
//	true  + 支持事务：冲突 → 整树回滚 → 重试（默认 1 次）→ 仍失败则报错
//	false（默认）  ：冲突 → 不重试，直接报错（无事务时重试会留半棵树）
//
// 为什么默认关闭：无事务部署（Mongo 单机）下重试无意义且有害；
// 把「有事务」当成需要显式声明的能力（B10），避免框架擅自重试。
func (tc *TxCoordinator) SetRetryOnPKConflict(v bool) *TxCoordinator {
	tc.retryOnPKConflict = v
	return tc
}

// SetMaxPKConflictRetries 设置冲突重试次数（默认 1；0 = 不重试）。
func (tc *TxCoordinator) SetMaxPKConflictRetries(n int) *TxCoordinator {
	if n < 0 {
		n = 0
	}
	tc.maxPKConflictRetries = n
	return tc
}

// RetryOnPKConflict 返回是否启用了主键冲突重试。
func (tc *TxCoordinator) RetryOnPKConflict() bool { return tc.retryOnPKConflict }

// Run 根据 ctx 中已有的存储类型自动选择事务。
// 如果 ctx 中已包含 mongo session → RunMongo；否则 → RunMySQL。
//
// 同时在此创建**事务级 remap catalog**（级联引用重映射 v2）：catalog 的生命周期
// 必须与「一次事务执行」严格对齐，而不是 HTTP request context —— 否则同一请求内
// 发生事务重试时会复用上一次失败事务残留的映射（见 WithRemapCatalog）。
func (tc *TxCoordinator) Run(ctx context.Context, fn func(txCtx context.Context) error) error {
	ctx, _ = WithRemapCatalog(ctx)
	if tc.mongoDB != nil && common.GetMongoSession(ctx) != nil {
		// 上下文中已有 mongo session → 不另开事务，直接执行（级联场景）
		return fn(ctx)
	}
	// 优先 MySQL
	if tc.db != nil {
		return tc.RunMySQL(ctx, fn)
	}
	if tc.mongoDB != nil {
		return tc.RunMongo(ctx, fn)
	}
	// 无任何 DB → 直接执行
	return fn(ctx)
}

// RunMySQL 在 GORM 事务内执行。
func (tc *TxCoordinator) RunMySQL(ctx context.Context, fn func(txCtx context.Context) error) error {
	ctx, _ = WithRemapCatalog(ctx)
	if tc.db == nil {
		return fn(ctx)
	}
	return tc.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := common.WithTx(ctx, tx)
		return fn(txCtx)
	})
}

// runWithPKRetry 按「显式声明的重试策略」执行一次事务体。
//
// 语义（设计文档 §7.2）：
//
//	retryOnPKConflict=true （支持事务的部署）：
//	    冲突 → 事务整体回滚 → 重试（默认 1 次）→ 仍失败则报错
//	retryOnPKConflict=false（无事务部署，默认）：
//	    冲突 → **不重试**，直接返回（由调用方做标删兜底 + ERROR 日志）
//
// 为什么默认不重试：无事务部署下重试会留下「半棵树」——
// 第一次尝试已写入的记录不会回滚，重试又写一份，脏数据比直接报错更糟（§7.3）。
//
// 每次重试都重新执行事务体（含预分配 → 装配 → 落库），因此 ULID 会重新生成 ——
// 这正是「重新发起整个请求」在框架内的等价形态（§7.4）。
func (tc *TxCoordinator) runWithPKRetry(ctx context.Context, fn func(txCtx context.Context) error) error {
	if !tc.retryOnPKConflict {
		return fn(ctx)
	}
	attempts := tc.maxPKConflictRetries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		// 每次尝试都清空「已落库清单」：上一次尝试的记录已随事务回滚，
		// 若不清空，标删兜底会去删一批根本不存在（或已回滚）的记录。
		if reg := preallocRegistryFrom(ctx); reg != nil {
			reg.ClearWritten()
		}
		lastErr = fn(ctx)
		if lastErr == nil {
			return nil
		}
		if !isPKConflictError(lastErr) {
			return lastErr // 非冲突错误不重试
		}
		if i < attempts-1 {
			logrus.Warnf("gocrux: 事务因主键冲突回滚，准备重试（第 %d/%d 次）: %v",
				i+1, tc.maxPKConflictRetries, lastErr)
		}
	}
	return lastErr
}

// RunWithPKRetry 公开入口：按 set 的重试策略执行事务体（供 Handler 调用）。
//
// 与 Run 的区别：Run 只负责「开事务」，本方法额外叠加「主键冲突整树重试」。
// 之所以分开而不是内建进 Run：Run 被大量既有路径调用（Get/List/Delete 等），
// 把重试语义塞进去会改变那些路径的行为。
func (tc *TxCoordinator) RunWithPKRetry(ctx context.Context, fn func(txCtx context.Context) error) error {
	return tc.runWithPKRetry(ctx, func(c context.Context) error {
		return tc.Run(c, fn)
	})
}

// RunMongo 在 MongoDB 事务内执行。
func (tc *TxCoordinator) RunMongo(ctx context.Context, fn func(txCtx context.Context) error) error {
	ctx, _ = WithRemapCatalog(ctx)
	if tc.mongoDB == nil {
		return fn(ctx)
	}
	client := tc.mongoDB.Client()
	sess, err := client.StartSession()
	if err != nil {
		return err
	}
	defer sess.EndSession(ctx)

	_, err = sess.WithTransaction(ctx, func(sc mongo.SessionContext) (interface{}, error) {
		return nil, fn(sc)
	})
	return err
}
