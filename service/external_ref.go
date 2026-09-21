package service

import (
	"context"
	"fmt"

	errs "github.com/Huey1979/gocrux/errors"
	"github.com/Huey1979/gocrux/repository"
)

// ============================================================
// 外部引用解析（应用方 §19.4 / §20.5）
//
// 问题：引用**已存在**的外部实体时，框架此前完全不介入 —— 前端传什么就存什么。
// 「只有 ULID」「只有 code」「ULID + code」三种形态被混成一种隐式行为，
// 于是「引用了旧版本」这类问题只能靠人工发现。
//
// 本文件提供**可复用的一步**：把「选择意图」（code 和/或 ULID）解析为
// 「权威身份」（ULID + code + 版本状态 + 版本号）。
//
// 边界（刻意不做的部分）——权限、数据归属、业务状态合法性、以及最终持久化
// 形态（PersistMode：只存 code / 只存 ULID / 存 {ulid, code}）都属业务语义，
// 由应用侧决定；框架只保证「解析出的 ULID 是权威版本」，不接受未经验证的
// 外部 ULID 直接作为权威引用。
// ============================================================

// GetPublishedByCode 按业务编码查询**正式发布版本**（version_status = published）。
//
// ⚠ 与 GetByCode 的区别（应用方 §20.3 需要的是本方法）：
//
//	GetByCode          ：is_current=1 的**当前工作版本** —— 编辑中的草稿同样
//	                     满足该条件（_doGetByCode 明确「不论是否 published」）；
//	GetPublishedByCode ：version_status='published' 的**线上生效版本**。
//
// 二者在「刚发布完」时指向同一行，一旦存在编辑中的草稿就会分叉；
// 引用要固定到线上生效版本时必须使用本方法。
//
// 退化规则（不报错，只是语义变弱）：
//   - 非版本化实体：没有 published 概念 → 退化为 GetByCode（按 code 等值查询）；
//   - 版本化但未配置 StatusField：无法判定状态 → 同样退化。
//
// 同一 code 理论上只有一条 published（其余应为 deprecated）；防御性地把候选
// 全部取回后按 published_at 选最新一条（pickLatestPublished），避免脏数据
// 导致结果不确定。
func (s *GenericService[M]) GetPublishedByCode(ctx context.Context, code string) (*M, error) {
	if code == "" {
		return nil, errs.ErrMissingParam("code")
	}
	vf := s.config.VersionFields
	if !s.config.VersionMode || vf == nil || vf.StatusField == "" {
		return s.GetByCode(ctx, code)
	}
	filters := []repository.Filter{
		{Field: resolveColumn[M](vf.CodeField), Op: repository.OpEQ, Value: code},
		{Field: resolveColumn[M](vf.StatusField), Op: repository.OpEQ, Value: string(VersionStatusPublished)},
	}
	// BUG-069 同口径：未启用软删时不拼 is_deleted，避免无该列的表报未知列。
	if delField, delVal, ok := s.DeletedColumn(); ok {
		filters = append(filters, repository.Filter{Field: delField, Op: repository.OpEQ, Value: delVal})
	}
	rows, _, err := s.repo.ListByFilters(ctx, repository.ListFilters{Filters: filters, PageSize: 0})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errs.ErrRecordNotFound
	}
	return &rows[s.pickLatestPublished(rows, vf)], nil
}

// RefIdentity 返回实体的引用身份四元组：主键值、业务 code、版本状态、版本号。
//
// 存在的意义：外部引用解析发生在 handler 层，而「版本字段映射」用的是
// **Go 字段名**，只有 service 知道。若让 handler 自己反射取名，就会出现第二份
// 「怎么取 code / 版本状态」的实现 —— 映射约定一变必然分叉（与 BUG-053/061
// 「同一解析逻辑各写一份」同类问题）。故由 service 单点提供。
//
// 未启用版本化时后三位为空串，主键仍返回。
func (s *GenericService[M]) RefIdentity(m *M) (ulid, code, versionStatus, versionCode string) {
	if m == nil {
		return
	}
	// 主键：先按仓储主键列名取，取不到时用「列名 → Go 字段名」反查
	// （BUG-035/060 同口径：PKField() 可能是 DB 列名，Go 字段名需反查）。
	if v := getFieldVal(m, s.repo.PKField()); v != nil {
		ulid = fmt.Sprint(v)
	}
	if ulid == "" {
		if goField := resolveColumnFromDB[M](s.repo.PKField()); goField != "" {
			ulid = getStrField(m, goField)
		}
	}
	vf := s.config.VersionFields
	if vf == nil {
		return
	}
	if vf.CodeField != "" {
		code = getStrField(m, vf.CodeField)
	}
	if vf.StatusField != "" {
		versionStatus = getStrField(m, vf.StatusField)
	}
	if vf.VersionField != "" {
		versionCode = getStrField(m, vf.VersionField)
	}
	return
}
