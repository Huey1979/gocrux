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

	// BeforeCreatePersist 保存前钩子（级联引用重映射的挂载点，可选）。
	//
	// 调用时点：**主键 ULID 已生成、记录尚未落库** ——
	// 这正是「构建旧 ULID → 新 ULID 映射并重写引用」的唯一正确时机
	// （落库后再修补会产生短暂可见的错误版本，且破坏事务一致性）。
	//
	// 与 BeforeCreate 的差别：BeforeCreate 收到的是**请求对象**（尚未 MergeTo 成实体、
	// 主键也未生成），拿不到新 ULID；本钩子收到的是**已生成主键的实体切片**，
	// 因此可用于修正实体间引用。
	//
	// 返回 error 会让整个创建（含事务）失败。
	BeforeCreatePersist func(ctx context.Context, entities []*M) error

	// -------- Update --------
	BeforeUpdate func(ctx context.Context, reqs []service.CrudRequest[M], parentVersioned bool) ([]service.CrudRequest[M], error)
	DoUpdate     func(ctx context.Context, reqs []service.CrudRequest[M], parentVersioned bool) ([]*M, error)
	AfterUpdate  func(ctx context.Context, results []*M, parentVersioned bool) ([]*M, error)

	// -------- BatchUpdate（SQL IN 统一赋值：{ids:[...], key:val, ...}） --------
	BeforeBatchUpdate func(ctx context.Context, ids []any, updates map[string]any) ([]any, map[string]any, error)
	DoBatchUpdate     func(ctx context.Context, ids []any, updates map[string]any) error
	AfterBatchUpdate  func(ctx context.Context, ids []any, updates map[string]any) error

	// -------- Delete --------
	BeforeDelete func(ctx context.Context, ids, codes any) (any, any, error)
	DoDelete     func(ctx context.Context, ids, codes any) error
	AfterDelete  func(ctx context.Context) error

	// -------- Restore（恢复已软删记录，BUG-069） --------
	// 仅把软删标记置回未删值，不改业务字段；需要修改已删记录时先 Restore 再 Update。
	BeforeRestore func(ctx context.Context, ids any) (any, error)
	DoRestore     func(ctx context.Context, ids any) error
	AfterRestore  func(ctx context.Context, ids any) error

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

	// -------- 整树装配前后：raw map 树规范化（应用方 §26.3） --------
	//
	// 为什么需要这对钩子：装配器按 **JSON 字段名**在 raw map 上做路径求值 /
	// Match / Assign，因此它要求「被装配的节点已经是对象/数组」。
	// 而以下两类数据仍可能是 **JSON 文本**（type:json 列 / 表单配置的 JSON 字段）：
	//
	//	① 请求体（历史调用方仍传字符串）；
	//	② 数据库存量数据与「版本化 update 未传子表」时的 DB 回填数据。
	//
	// 框架**不猜**哪个字符串是 JSON（普通字符串若恰好长得像 JSON 必须保持原样），
	// 因此由应用按自己的 schema 实现这一步。
	//
	// 契约（与执行时机的对应关系，逐层触发）：
	//
	//	BeforeCascadePrepare  本层记录即将被展开/预分配/装配**之前**。
	//	                      必须「浅到深」：外层 JSON 文本不先解码，内层路径根本不存在。
	//	                      收到的 rawMaps 就是**后续 _doCreate/_doUpdate、
	//	                      预分配与装配实际读取的同一批 map 句柄**（原地修改即可）。
	//	AfterCascadeAssemble  本层（含更深层）的装配**全部完成之后、落库之前**。
	//	                      必须「深到浅」：先把内层恢复成字符串，再序列化包含它的外层
	//	                      （框架按后序调用，子层先于父层）。
	//
	// 触发范围：仅在**装配通道启用**（该请求挂了预分配注册表）时触发；
	// 未启用装配的调用方行为完全不变（零影响）。
	BeforeCascadePrepare func(ctx context.Context, rawMaps []map[string]any) error
	AfterCascadeAssemble func(ctx context.Context, rawMaps []map[string]any) error
}
