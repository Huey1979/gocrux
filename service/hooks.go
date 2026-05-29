package service

import "context"

// ============================================================
// Hooks 钩子族
// 每个操作对应 before / do / after 三个钩子。
// 外部可覆盖任意钩子；未设置时 fallback 到 GenericService 内置实现。
// 钩子通过闭包捕获 Service 实例，可访问 repo、config、request 等。
// ctx 从 Handler → Service → hooks 全链路透传。
// ============================================================

type Hooks[M Record] struct {
	// -------- Create --------
	BeforeCreate func(ctx context.Context, input []CrudRequest[M]) ([]*M, error)
	DoCreate     func(ctx context.Context, input []*M) ([]*M, error)
	AfterCreate  func(ctx context.Context, result []*M) ([]*M, error)

	// -------- Update --------
	BeforeUpdate func(ctx context.Context, id, data any) (any, any, error)
	DoUpdate     func(ctx context.Context, id, data any) (*M, error)
	AfterUpdate  func(ctx context.Context, id any, result *M, pdata any) (*M, error)

	// -------- Delete --------
	BeforeDelete func(ctx context.Context, ids, codes any) (any, any, error)
	DoDelete     func(ctx context.Context, id, data any) error
	AfterDelete  func(ctx context.Context, id, data any) error

	// -------- Get --------
	BeforeGet func(ctx context.Context, id any) (any, error)
	DoGet     func(ctx context.Context, id any) (*M, error)
	AfterGet  func(ctx context.Context, result *M) (*M, error)

	// -------- List --------
	BeforeList func(ctx context.Context, query any) (any, error)
	DoList     func(ctx context.Context, query any) ([]M, int64, error)
	AfterList  func(ctx context.Context, list []M, total int64) ([]M, int64, error)

	// -------- Activate（激活版本：发布 / 回滚） --------
	BeforeActivate func(ctx context.Context, id any) (any, error)
	DoActivate     func(ctx context.Context, id any) error
	AfterActivate  func(ctx context.Context, id any) error

	// -------- ListVersions --------
	BeforeListVersions func(ctx context.Context, id any, code string) (any, error)
	DoListVersions     func(ctx context.Context, id any) ([]M, error)
	AfterListVersions  func(ctx context.Context, result []M) ([]M, error)

	// -------- EditVersion（版本元数据修改：状态、备注） --------
	BeforeEditVersion func(ctx context.Context, id any, patches map[string]any) (any, map[string]any, error)
	DoEditVersion     func(ctx context.Context, id any, patches map[string]any) (*M, error)
	AfterEditVersion  func(ctx context.Context, id any, result *M) (*M, error)
}
