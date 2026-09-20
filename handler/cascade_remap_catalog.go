package handler

import (
	"context"
	"fmt"
	"sort"

	errs "github.com/Huey1979/gocrux/errors"
)

// ============================================================
// 事务级引用映射 catalog（v2）
//
// 解决的问题：v1 的 Remaps 映射表只在「当前这一个级联关系的 childData 批次」
// 内构建，因此只能重写同一批子记录内部的引用。表单域存在跨级联分支的引用：
//
//	form
//	  ├─ write_section → write_field     ← 新 ULID 的生成源（发布方）
//	  ├─ list_column.field_ulid          ┐
//	  ├─ detail_section → detail_field   ├ 消费方（跨 Handler 边界）
//	  └─ validation.error_on[].field_ulid┘
//
// 版本化发布时这些子表都会被重建为新 ULID。若消费方拿不到 write_field 批次
// 产生的「旧 ULID → 新 ULID」，新版本就会静默引用旧版本的字段。
//
// 机制：一次级联事务内维护一个命名空间 → 映射表的 catalog，随 context 向下
// 穿透所有子 Handler。发布方在其批次执行完毕后 Publish，消费方在处理前 Resolve。
//
// 生命周期严格绑定 ctx（即事务）：不跨请求、不跨事务 —— 同一请求内若发生重试，
// 新事务会拿到全新的空 catalog（见 WithRemapCatalog 的注释）。
// ============================================================

// remapCatalogCtxKey catalog 的 context key。
type remapCatalogCtxKey struct{}

// RemapSnapshot 某个命名空间在「Resolve 调用时刻」的映射快照（只读）。
//
// 注意语义：这是**调用时刻的合并结果**，不是实时视图。若消费方 A 与消费方 B
// 之间又有新的发布方批次写入同一命名空间，B 会看到比 A 更新的内容。
//
// 对 heims 表单域这一形态：write_section 整批处理完才返回 form，随后
// list_column → detail_field → validation 依次消费，因此三者看到的必然是
// 同一份完整映射。故当前不引入快照版本号。
type RemapSnapshot struct {
	// key 命名空间（即 CascadeRelation.RemapKey）。
	key string

	// oldToNew 旧 ULID → 新 ULID（权威映射）。
	oldToNew map[string]string
	// codeToNew 业务 code → 新 ULID（兜底映射）。
	codeToNew map[string]string

	// publishers 已向本命名空间发布过的发布方 Handler 名（去重，供错误文案与排查）。
	publishers []string
}

// RemapCatalog 事务级映射目录。
//
// 并发安全说明：级联事务在单个 goroutine 内串行执行（_doCreate/_doUpdate 的
// 分支循环是顺序 for），故**不加锁**。这把「事务边界 = 单 goroutine」的既有
// 前提显式化：若将来出现并行级联分支，必须在此处补锁。
type RemapCatalog struct {
	// namespaces 命名空间 → 快照。
	namespaces map[string]*RemapSnapshot
}

// NewRemapCatalog 创建空 catalog。
func NewRemapCatalog() *RemapCatalog {
	return &RemapCatalog{namespaces: make(map[string]*RemapSnapshot)}
}

// WithRemapCatalog 把 catalog 挂到 ctx（幂等：已存在则原样返回）。
//
// 生命周期约束：catalog 必须绑定**一次事务**，而不是 HTTP request context。
// 若同一请求内事务重试，应在重试入口重新 WithRemapCatalog —— 否则会复用
// 上一次失败事务残留的映射（脏数据）。当 catalog 由 TxCoordinator 的 Run
// 入口创建时，该约束自动成立。
func WithRemapCatalog(ctx context.Context) (context.Context, *RemapCatalog) {
	if c := remapCatalogFrom(ctx); c != nil {
		return ctx, c
	}
	c := NewRemapCatalog()
	return context.WithValue(ctx, remapCatalogCtxKey{}, c), c
}

// remapCatalogFrom 从 ctx 取 catalog（未挂载返回 nil）。
func remapCatalogFrom(ctx context.Context) *RemapCatalog {
	c, _ := ctx.Value(remapCatalogCtxKey{}).(*RemapCatalog)
	return c
}

// Publish 向命名空间发布一批「旧 ULID → 新 ULID」与「code → 新 ULID」映射。
//
// 合并规则（与 heims 共识 §7.3 一致）：
//   - 不同旧 ULID → 不同新 ULID：正常追加；
//   - 同一 code → **相同**新 ULID：幂等，忽略；
//   - 同一 code → **不同**新 ULID：报错（code 作为跨批次关联键必须唯一）；
//   - 同一旧 ULID → **不同**新 ULID：报错（同一旧记录被两批错误重建为不同新记录，
//     若静默取其一，另一批的引用会指向错误的记录）。
//
// 空 key / 空 value 不参与映射（调用方已过滤，此处再兜一层）。
// 返回的错误包装 errs.ErrRemapInconsistent，调用方应直接让事务失败。
func (c *RemapCatalog) Publish(key string, oldToNew, codeToNew map[string]string, publisher string) error {
	if c == nil || key == "" {
		return nil
	}
	if len(oldToNew) == 0 && len(codeToNew) == 0 {
		return nil
	}
	snap := c.namespace(key)

	// 排序后遍历，保证冲突报错内容稳定（map 遍历顺序随机 → 错误信息不确定会干扰测试）
	for _, oldULID := range sortedKeys(oldToNew) {
		newULID := oldToNew[oldULID]
		if oldULID == "" || newULID == "" {
			continue
		}
		if prev, ok := snap.oldToNew[oldULID]; ok {
			if prev != newULID {
				return fmt.Errorf(
					"%w: 命名空间 %q 中旧 ULID=%s 已被映射为 %s，本次发布又映射为 %s"+
						"（发布方 %s；同一旧记录被重建为不同新记录，不能静默取其一）",
					errs.ErrRemapInconsistent, key, oldULID, prev, newULID, publisher)
			}
			continue // 幂等
		}
		snap.oldToNew[oldULID] = newULID
	}

	for _, code := range sortedKeys(codeToNew) {
		newULID := codeToNew[code]
		if code == "" || newULID == "" {
			continue
		}
		if prev, ok := snap.codeToNew[code]; ok {
			if prev != newULID {
				return fmt.Errorf(
					"%w: 命名空间 %q 中 code=%s 已被映射为 %s，本次发布又映射为 %s"+
						"（发布方 %s；同 code 字段必须唯一）",
					errs.ErrRemapInconsistent, key, code, prev, newULID, publisher)
			}
			continue // 幂等
		}
		snap.codeToNew[code] = newULID
	}

	snap.addPublisher(publisher)
	return nil
}

// Resolve 取命名空间在**调用时刻**的只读合并快照。
//
// 语义为只读（非破坏性）：多个消费方（list_column / detail_field / validation）
// 可以重复 Resolve 同一个 key，互不影响。返回 false 表示该命名空间尚无任何发布方。
func (c *RemapCatalog) Resolve(key string) (*RemapSnapshot, bool) {
	if c == nil || key == "" {
		return nil, false
	}
	snap, ok := c.namespaces[key]
	if !ok {
		return nil, false
	}
	return snap, true
}

// Keys 返回当前已发布的全部命名空间（已排序，供诊断/测试用）。
func (c *RemapCatalog) Keys() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.namespaces))
	for k := range c.namespaces {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// namespace 取（或创建）命名空间。
func (c *RemapCatalog) namespace(key string) *RemapSnapshot {
	snap, ok := c.namespaces[key]
	if !ok {
		snap = &RemapSnapshot{
			key:       key,
			oldToNew:  make(map[string]string),
			codeToNew: make(map[string]string),
		}
		c.namespaces[key] = snap
	}
	return snap
}

// addPublisher 记录发布方（去重，保持首次出现的顺序）。
func (s *RemapSnapshot) addPublisher(name string) {
	if s == nil || name == "" {
		return
	}
	for _, p := range s.publishers {
		if p == name {
			return
		}
	}
	s.publishers = append(s.publishers, name)
}

// Publishers 返回已发布过的发布方 Handler 名（副本）。
func (s *RemapSnapshot) Publishers() []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s.publishers))
	copy(out, s.publishers)
	return out
}

// toRemapPlan 把快照转成本次消费的重映射计划（深拷贝映射表，避免消费过程中
// 被后续 Publish 影响 —— 快照语义的落地）。
//
// bindings 取自消费方的 ReferenceRemap.Bindings。
func (s *RemapSnapshot) toRemapPlan(handlerName string, bindings []ReferenceBinding, childData []map[string]any) *RemapPlan {
	if s == nil || len(bindings) == 0 {
		return nil
	}
	plan := &RemapPlan{
		oldToNew:    make(map[string]string, len(s.oldToNew)),
		codeToNew:   make(map[string]string, len(s.codeToNew)),
		bindings:    bindings,
		handlerName: handlerName,
		childData:   childData,
	}
	for k, v := range s.oldToNew {
		plan.oldToNew[k] = v
	}
	for k, v := range s.codeToNew {
		plan.codeToNew[k] = v
	}
	return plan
}

// sortedKeys 返回 map 的键（已排序），让冲突报错内容稳定可测。
func sortedKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
