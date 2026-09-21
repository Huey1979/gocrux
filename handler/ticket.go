package handler

import (
	"context"

	"github.com/Huey1979/gocrux/common"
	"github.com/Huey1979/gocrux/service"
)

// ============================================================
// ticket 闭环 —— 「ULID 是我们自己生成的」的凭证
//
// 背景（设计文档 §三）：
//
//	service._beforeCreate 的现状是「PK 非空就信任」——包括前端传来的。
//	预分配方案要求「请求入口为整棵树分配 ULID，落库时直接信任」，
//	但若没有凭证，前端伪造的 ULID 同样会被信任，预分配就失去意义。
//
// 设计（应用方 §13.1，已采纳）：
//
//	默认实现为 **ctx 注册表成员校验**：
//	  · 注册表只在服务进程内存中、随 ctx 传递，外部请求**没有任何途径**注入；
//	  · 随 ctx 释放 —— 无 TTL、无跨请求复用、无清理负担；
//	  · 因此「成员校验」与「签名校验」在本请求范围内的安全强度等价。
//
//	HMAC / Redis 等更强保障作为**可插拔实现**保留（见 TicketVerifier）：
//	gocrux 是通用框架，不排除其他使用方有跨进程凭证、离线验证等场景。
//	heims 不需要 HMAC，故默认零配置即可工作。
// ============================================================

// ticketCtxKey ticket 的 context key。
type ticketCtxKey struct{}

// ticketRegistryCtxKey 预分配注册表的 context key。
type ticketRegistryCtxKey struct{}

// TicketVerifier 凭证签发与校验（可插拔扩展点）。
//
// 默认实现为 ctx 注册表（ticketRegistryVerifier），开箱即用、零配置；
// 需要更强保障时用 SetTicketVerifier 替换（HMAC 签名 / Redis 共享等）。
//
// 三种实现的**语义等价**（都是"这个 ULID 是不是我们自己签发的"），
// 切换实现不改任何调用方 —— 只在 service 落库校验处被引用。
type TicketVerifier interface {
	// Issue 为一批 ULID 签发凭证。
	Issue(ticket string, ulids []string) error
	// Verify 校验某个 ULID 是否由本 ticket 签发。
	Verify(ticket, ulid string) bool
}

// defaultTicketVerifier 包级默认实现（ctx 注册表）。
//
// 单例而非每 ctx 一个：它自身无状态，真正的状态存在 ctx 内的注册表里。
var defaultTicketVerifier TicketVerifier = ticketRegistryVerifier{}

// customTicketVerifier 应用自定义的凭证实现（SetTicketVerifier 注入，nil = 用默认）。
var customTicketVerifier TicketVerifier

// SetTicketVerifier 替换全局凭证实现（可选）。
//
// 传 nil 恢复默认的 ctx 注册表实现。仅影响后续请求，幂等。
func SetTicketVerifier(v TicketVerifier) {
	customTicketVerifier = v
}

// activeVerifier 返回当前生效的凭证实现。
func activeVerifier() TicketVerifier {
	if customTicketVerifier != nil {
		return customTicketVerifier
	}
	return defaultTicketVerifier
}

// ============================================================
// ctx 注册表实现（默认）
// ============================================================

// PreallocRegistry 请求级预分配注册表。
//
// 记录本次请求中框架**自己生成**的全部 ULID，并按 (Target 标识, Match 键值)
// 建立索引，供装配阶段查找目标记录。
//
// 并发安全说明：与 RemapCatalog 同理 —— 一次请求的级联处理在单个 goroutine
// 内串行执行（_doCreate/_doUpdate 的分支循环是顺序 for），故**不加锁**。
// 这把「请求边界 = 单 goroutine」的前提显式化。
type PreallocRegistry struct {
	// ulids 本次请求生成的全部 ULID（凭证校验的依据）。
	ulids map[string]bool

	// byTarget Target 标识（对应 v2 的 RemapKey）→ 该分支的记录列表。
	//
	// 装配的 Target 留空表示「本批次自身」，此时走 batchLocal 而不是这里。
	byTarget map[string][]*assembledRecord

	// batchLocal 本批次自身的记录（Target 留空时使用）。
	//
	// 用 []*assembledRecord 而不是 []map[string]any：装配需要「按 Match 键
	// 读取记录字段」与「按 Assign 写回」，而 map 与实体是两个形态
	// （map 是 JSON 往返的结果，落库走实体）。持有 pair 才能同时更新两侧。
	batchLocal []*assembledRecord

	// written 本次请求已落库的记录标识（entityType + ":" + ulid），
	// 供无事务部署下的主键冲突兜底清理使用（设计文档 §7.3）。
	written []writtenRecord
}

// assembledRecord 一条参与装配的记录：map 形态与实体形态并存。
//
// 为什么两者都要：
//   - map 是**装配读写**的载体（路径求值 / Match / Assign 都按 JSON 字段名）；
//   - 实体是**落库**的载体（_doCreate 只读实体）。
//
// 装配发生在「预分配之后、落库之前」，此时 map 尚未 MergeTo 进实体
// （子 Handler 的 _beforeCreate 才是 MergeTo 时点），因此两侧都持有句柄，
// 装配完把 map 的改动同步回实体。
type assembledRecord struct {
	// entityType 记录所属的子 Handler 名（用于错误定位与清理范围）。
	entityType string
	// data JSON 形态的数据（装配按 JSON 字段名读写）。
	data map[string]any
	// entity 实体形态的指针（落库载体）；可能为 nil（尚未 MergeTo）。
	entity any
	// pkField 主键列名（装配写回实体时需要）。
	pkField string
}

// writtenRecord 本次请求已落库的一条记录（无事务冲突兜底清理用）。
type writtenRecord struct {
	entityType string
	ulid       string
}

// NewPreallocRegistry 创建空的预分配注册表。
func NewPreallocRegistry() *PreallocRegistry {
	return &PreallocRegistry{
		ulids:    make(map[string]bool),
		byTarget: make(map[string][]*assembledRecord),
	}
}

// WithPreallocRegistry 把注册表挂到 ctx（幂等：已存在则原样返回）。
//
// 生命周期：注册表绑定**一次请求**（顶层 Handler 入口创建，随 ctx 下传）。
// 与 RemapCatalog 绑事务不同 —— 预分配需要在事务之前完成（阶段 1），
// 且重试语义是「前端重新发起整个请求」（新 ctx → 新注册表），
// 故绑请求而非绑事务是正确的。
func WithPreallocRegistry(ctx context.Context) (context.Context, *PreallocRegistry) {
	if r := preallocRegistryFrom(ctx); r != nil {
		return ctx, r
	}
	r := NewPreallocRegistry()
	return context.WithValue(ctx, ticketRegistryCtxKey{}, r), r
}

// preallocRegistryFrom 从 ctx 取注册表（未挂载返回 nil）。
func preallocRegistryFrom(ctx context.Context) *PreallocRegistry {
	r, _ := ctx.Value(ticketRegistryCtxKey{}).(*PreallocRegistry)
	return r
}

// EnsurePreallocRegistry 确保 ctx 中有注册表与 ticket，返回新 ctx 与注册表。
//
// 供顶层 Handler 入口调用：已有则复用（级联子 Handler 会看到同一份）。
func EnsurePreallocRegistry(ctx context.Context) (context.Context, *PreallocRegistry) {
	if r := preallocRegistryFrom(ctx); r != nil {
		return ctx, r
	}
	ctx, r := WithPreallocRegistry(ctx)
	return WithTicket(ctx, newTicket()), r
}

// ============================================================
// ticket 传递
// ============================================================

// WithTicket 把 ticket 写入 ctx。
func WithTicket(ctx context.Context, ticket string) context.Context {
	return context.WithValue(ctx, ticketCtxKey{}, ticket)
}

// ticketFrom 从 ctx 取 ticket（未设置返回空串）。
func ticketFrom(ctx context.Context) string {
	t, _ := ctx.Value(ticketCtxKey{}).(string)
	return t
}

// newTicket 生成一个新的 ticket。
//
// 语义：**不保密，仅作请求内关联值**（用于把「签发」与「校验」两步串起来，
// 以及错误日志里标识是哪次请求）。16 字节随机量取自 crypto/rand。
func newTicket() string {
	return common.NewULID()
}

// ============================================================
// 注册表操作
// ============================================================

// Register 登记一个框架生成的 ULID（凭证校验的依据）。
func (r *PreallocRegistry) Register(ulid string) {
	if r == nil || ulid == "" {
		return
	}
	r.ulids[ulid] = true
}

// RegisterAll 批量登记。
func (r *PreallocRegistry) RegisterAll(ulids []string) {
	for _, u := range ulids {
		r.Register(u)
	}
}

// Has 判断某个 ULID 是否由本次请求的框架生成（凭证校验）。
func (r *PreallocRegistry) Has(ulid string) bool {
	if r == nil || ulid == "" {
		return false
	}
	return r.ulids[ulid]
}

// Count 返回已登记的 ULID 数量（诊断/测试用）。
func (r *PreallocRegistry) Count() int {
	if r == nil {
		return 0
	}
	return len(r.ulids)
}

// AddTarget 把一个记录登记到指定 Target 分支的索引。
//
// target 为空串表示「本批次自身」（批内自引用），此时进 batchLocal。
func (r *PreallocRegistry) AddTarget(target string, rec *assembledRecord) {
	if r == nil || rec == nil {
		return
	}
	if target == "" {
		r.batchLocal = append(r.batchLocal, rec)
		return
	}
	r.byTarget[target] = append(r.byTarget[target], rec)
}

// TargetRecords 取某个 Target 分支的全部记录（不存在返回 nil）。
func (r *PreallocRegistry) TargetRecords(target string) []*assembledRecord {
	if r == nil || target == "" {
		return nil
	}
	return r.byTarget[target]
}

// Targets 返回已登记的 Target 标识（去重，顺序不保证；供诊断与 L1 校验）。
func (r *PreallocRegistry) Targets() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.byTarget))
	for k := range r.byTarget {
		out = append(out, k)
	}
	return out
}

// MarkWritten 记录一条已落库的记录（无事务冲突兜底清理用）。
func (r *PreallocRegistry) MarkWritten(entityType, ulid string) {
	if r == nil || ulid == "" {
		return
	}
	r.written = append(r.written, writtenRecord{entityType: entityType, ulid: ulid})
}

// Written 返回本次请求已落库的记录清单（副本）。
func (r *PreallocRegistry) Written() []writtenRecord {
	if r == nil {
		return nil
	}
	out := make([]writtenRecord, len(r.written))
	copy(out, r.written)
	return out
}

// ClearWritten 清空已落库清单（重试前调用，避免把上次的记录算进清理范围）。
func (r *PreallocRegistry) ClearWritten() {
	if r == nil {
		return
	}
	r.written = nil
}

// ============================================================
// 默认凭证实现（ctx 注册表成员校验）
// ============================================================

// ticketRegistryVerifier 基于 ctx 注册表的凭证实现（默认）。
//
// Issue 语义：把 ULID 记入 ctx 注册表。由于 Verify 需要能拿到同一份注册表，
// 而 TicketVerifier 接口只有 (ticket, ulid) 两个参数（为兼容 HMAC/Redis 等
// 状态外置的实现），因此本实现**通过 ctx 获取注册表** —— 见 issueWithContext
// / verifyWithContext：框架内部走带 ctx 的路径，接口方法本身作为降级。
type ticketRegistryVerifier struct{}

// Issue 实现 TicketVerifier。
//
// 注意：ctx 注册表实现**必须**经 issueWithContext 调用才能生效
// （接口签名拿不到 ctx）。此处为接口完整性保留，直接调用无效果。
func (ticketRegistryVerifier) Issue(string, []string) error { return nil }

// Verify 实现 TicketVerifier。同上，需经 verifyWithContext。
func (ticketRegistryVerifier) Verify(string, string) bool { return false }

// issueWithContext 签发凭证（带 ctx 版本，框架内部使用）。
//
// 默认实现走 ctx 注册表；自定义实现走接口方法。
func issueWithContext(ctx context.Context, ticket string, ulids []string) error {
	if customTicketVerifier != nil {
		return customTicketVerifier.Issue(ticket, ulids)
	}
	if r := preallocRegistryFrom(ctx); r != nil {
		r.RegisterAll(ulids)
	}
	return nil
}

// verifyWithContext 校验凭证（带 ctx 版本，框架内部使用）。
//
// 优先级：
//  1. 自定义实现（SetTicketVerifier 注入）→ 走其 Verify；
//  2. 默认实现 → ctx 注册表成员校验。
//
// **没有注册表也没有自定义实现时返回 true**：那说明调用方没有走
// 「预分配通道」（例如直接调用 service，或老代码路径），此时保持
// 既有「非空即信任」语义，避免把非预分配场景一刀切成拒绝 ——
// 那会破坏所有未迁移的调用方（向后兼容是硬约束）。
func verifyWithContext(ctx context.Context, ticket, ulid string) bool {
	if ulid == "" {
		return false
	}
	if customTicketVerifier != nil {
		return customTicketVerifier.Verify(ticket, ulid)
	}
	r := preallocRegistryFrom(ctx)
	if r == nil {
		return true // 非预分配路径 → 保持既有语义
	}
	return r.Has(ulid)
}

// InstallTicketBridge 在 ctx 上挂载凭证校验桥，返回新 ctx。
//
// 桥的实际载体在 **service 包**（service.WithPKTrustChecker）：Go 的包依赖
// 是单向的（handler → service），service 落库时不能反向调用 handler。
// 因此这里把"校验逻辑"包成 service 认识的函数类型挂上去。
//
// 语义与 verifyWithContext 一致：无注册表且无自定义实现时视为「非预分配
// 通道」→ 返回 true，保持既有「非空即信任」行为（向后兼容）。
func InstallTicketBridge(ctx context.Context) context.Context {
	reg := preallocRegistryFrom(ctx)
	// 未启用预分配通道（无注册表）时不挂桥：service 侧的校验函数会走
	// "无桥 → 视为可信"的向后兼容分支，行为与改造前完全一致。
	if reg == nil && customTicketVerifier == nil {
		return ctx
	}
	ticket := ticketFrom(ctx)
	return service.WithPKTrustChecker(ctx, func(ulid string) bool {
		return verifyWithContext(ctx, ticket, ulid)
	})
}
