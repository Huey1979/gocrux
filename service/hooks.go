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

	// BeforeCreatePersist 落库前钩子（可选）：
	// 在 `_beforeCreate` 完成（主键 ULID 已生成、审计列已填）之后、
	// `_doCreate` 真正 INSERT 之前调用。
	//
	// 适用场景：修正实体之间的引用（版本化级联重建时的引用重映射）——
	// 这是唯一能同时看到「新生成的主键」与「尚未落库」的时点，
	// 落库后再修补会产生短暂可见的错误版本，也破坏事务一致性。
	//
	// 返回 error 会让本次 Create（含外层事务）整体失败。
	BeforeCreatePersist func(ctx context.Context, entities []*M) error

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

// HooksSnapshot 返回当前 Service 层钩子的副本（值语义，可安全增改后经 SetHooks 写回）。
//
// 供 Handler 在不覆盖应用已注册钩子的前提下，追加需要「落库前」时点的能力
// （如版本化级联引用重映射，见 handler/cascade_remap.go）：
//
//	h := svc.HooksSnapshot()
//	h.BeforeCreatePersist = myHook
//	svc.SetHooks(h)
func (s *GenericService[M]) HooksSnapshot() Hooks[M] { return s.hooks }
