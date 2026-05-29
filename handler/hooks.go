package handler

import (
	"context"

	"github.com/Huey1979/gocrux/service"
)

// ============================================================
// HandlerHooks 钩子族
//
// 每个操作对应 before / do / after 三个钩子。
// 外部可覆盖任意钩子；未设置时 fallback 到 GenericHandler 内置实现。
//
// 【重要】before / after 钩子不依赖 *gin.Context ——
// 这意味着无论走 HTTP 入口还是级联入口，钩子都能正常工作。
//
// do 钩子负责调用 Service + 级联编排（事务由 TxCoordinator 保证）。
//
// ctx 从 Handler → hooks 全链路透传。
// ============================================================

type HandlerHooks[M service.Record] struct {
	// -------- Create --------
	BeforeCreate func(ctx context.Context, input []service.CrudRequest[M]) ([]service.CrudRequest[M], error)
	DoCreate     func(ctx context.Context, input []service.CrudRequest[M]) ([]*M, error)
	AfterCreate  func(ctx context.Context, result []*M) ([]*M, error)

	// -------- Update --------
	BeforeUpdate func(ctx context.Context, reqs []service.CrudRequest[M], parentVersioned bool) ([]service.CrudRequest[M], error)
	DoUpdate     func(ctx context.Context, reqs []service.CrudRequest[M], parentVersioned bool) ([]*M, error)
	AfterUpdate  func(ctx context.Context, results []*M, parentVersioned bool) ([]*M, error)

	// -------- Delete --------
	BeforeDelete func(ctx context.Context, ids, codes any) (any, any, error)
	DoDelete     func(ctx context.Context, ids, codes any) error
	AfterDelete  func(ctx context.Context) error

	// -------- Get --------
	BeforeGet func(ctx context.Context, req *GetRequest) (*GetRequest, error)
	DoGet     func(ctx context.Context, req *GetRequest) (map[string]any, error)
	AfterGet  func(ctx context.Context, result map[string]any) (map[string]any, error)

	// -------- List --------
	BeforeList func(ctx context.Context, query any) (any, error)
	DoList     func(ctx context.Context, query any, followPublished bool) ([]map[string]any, int64, error)
	AfterList  func(ctx context.Context, list []map[string]any, total int64) ([]map[string]any, int64, error)

	// -------- Activate（激活版本：发布 / 回滚） --------
	BeforeActivate func(ctx context.Context, id any) (any, error)
	DoActivate     func(ctx context.Context, id any) error
	AfterActivate  func(ctx context.Context) error

	// -------- ListVersions --------
	BeforeListVersions func(ctx context.Context, id any, code string) (any, string, error)
	DoListVersions     func(ctx context.Context, id any, code string) ([]M, error)
	AfterListVersions  func(ctx context.Context, result []M) ([]M, error)

	// -------- EditVersion（版本元数据修改：状态、备注） --------
	BeforeEditVersion func(ctx context.Context, id any, patches map[string]any) (any, map[string]any, error)
	DoEditVersion     func(ctx context.Context, id any, patches map[string]any) (*M, error)
	AfterEditVersion  func(ctx context.Context, result *M) (*M, error)
}
