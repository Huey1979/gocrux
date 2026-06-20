package handler

import (
	"context"

	"github.com/Huey1979/gocrux/common"

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
}

// NewTxCoordinator 创建事务编排器。db / mongoDB 可各自为 nil。
func NewTxCoordinator(db *gorm.DB, mongoDB *mongo.Database) *TxCoordinator {
	return &TxCoordinator{db: db, mongoDB: mongoDB}
}

// Run 根据 ctx 中已有的存储类型自动选择事务。
// 如果 ctx 中已包含 mongo session → RunMongo；否则 → RunMySQL。
func (tc *TxCoordinator) Run(ctx context.Context, fn func(txCtx context.Context) error) error {
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
	if tc.db == nil {
		return fn(ctx)
	}
	return tc.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := common.WithTx(ctx, tx)
		return fn(txCtx)
	})
}

// RunMongo 在 MongoDB 事务内执行。
func (tc *TxCoordinator) RunMongo(ctx context.Context, fn func(txCtx context.Context) error) error {
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
