package handler

import (
	"fmt"

	errs "github.com/Huey1979/gocrux/errors"
)

// ============================================================
// 版本化重建时的子记录字段合并（CascadeRelation.MergeChildrenOnVersionedRebuild）
//
// 背景（下游 BUG：cascade_partial_child）：
//
// 版本化父表 update 携带子表时，框架把请求子数据**整体**当作新版本的子快照
// （清 PK → CREATE 重建）。若调用方按「提交哪些字段就改哪些字段」的契约只提交
// 了部分字段，未提交字段在新快照里会退化为零值/默认值 —— 旧值静默丢失：
//
//	旧子记录: {col_code:"c1", title:"列1", unit:"px", width:120, expr:"a+b"}
//	请求携带: {ulid:"<旧ULID>", expr:"a-b"}
//	重建结果: {col_code:"c1", title:"", unit:"", width:0, expr:"a-b"}   ← title/unit/width 丢失
//
// 合并语义（置位后）：
//
//	① 按身份把请求子记录与旧子记录配对（旧主键 → 业务 code 兜底）；
//	② 配对成功 → 以旧记录为基底，用请求字段逐键覆盖；
//	③ 未配对   → 视为**新增**，按请求字段创建；
//	④ 请求数组里没有的旧子记录 → 不进入新版本（即删除；数组即子记录集合）。
//
// 「字段未传」与「字段显式置空」由 JSON 的 key 存在性区分：
// key 不存在 → 保留旧值；key 存在（含 null / ""）→ 覆盖，即显式清空。
//
// 只做「匹配 + 合并」，不碰主键与落库：新快照的 PK 仍由调用方清 PK 后重建
// （v3 通道填预分配值，否则由 service 生成），旧子行不做任何修改。
// ============================================================

// mergeChildrenByOldIdentity 把请求携带的子记录与旧子记录按身份合并。
//
//	incoming    请求携带的子记录（**不修改**，返回新 map）
//	old         DB 拉回的旧子记录（同一父下的当前有效行）
//	pkField     子实体主键列名（PKField()，读取时兼容约定 JSON 名 "ulid"，见 BUG-060）
//	codeField   业务 code 字段名（CascadeRelation.PublishCodeField；留空则只按主键匹配）
//
// 返回与 incoming 同序的新切片：配对的记录是「旧记录 ⊕ 请求字段」的新 map，
// 未配对的记录原样返回（由调用方清 PK 后按新增创建）。
//
// 身份歧义（同一业务 code 对应多条旧记录）时报 errs.ErrAssemblyIdentityAmbiguous ——
// 与「旧主键重复」同口径：绝不按位置猜测对应关系。
func mergeChildrenByOldIdentity(
	incoming, old []map[string]any,
	pkField, codeField, handlerName string,
) ([]map[string]any, error) {
	if len(incoming) == 0 || len(old) == 0 {
		return incoming, nil
	}

	byPK := make(map[string]map[string]any, len(old))
	byCode := make(map[string][]map[string]any, len(old))
	for _, o := range old {
		if o == nil {
			continue
		}
		if pk := recordIdentityPK(o, pkField); pk != "" {
			if _, dup := byPK[pk]; !dup {
				byPK[pk] = o
			}
		}
		if codeField != "" {
			if code := recordIdentityCode(o, codeField); code != "" {
				byCode[code] = append(byCode[code], o)
			}
		}
	}

	out := make([]map[string]any, 0, len(incoming))
	for _, inc := range incoming {
		if inc == nil {
			out = append(out, inc)
			continue
		}

		// 身份匹配：① 旧主键；② 业务 code（仅在主键未命中时兜底）
		var base map[string]any
		if pk := recordIdentityPK(inc, pkField); pk != "" {
			base = byPK[pk]
		}
		if base == nil && codeField != "" {
			code := recordIdentityCode(inc, codeField)
			// 注意：必须显式列出 len(cands)==0 —— 否则会落进 default 被误判为
			// 「code 歧义」（实测：新增子记录 c3 在旧记录里当然查不到）。
			switch cands := byCode[code]; {
			case code == "", len(cands) == 0:
				// 请求记录没有 code，或旧记录里没有该 code → 无从匹配，按新增处理
			case len(cands) == 1:
				base = cands[0]
			default:
				return nil, fmt.Errorf(
					"%w: Handler %s 的旧子记录中 %s=%q 对应 %d 条，无法确定与请求子记录的"+
						"对应关系（合并身份键必须唯一，请检查旧数据或改用主键提交）",
					errs.ErrAssemblyIdentityAmbiguous, handlerName, codeField, code, len(cands))
			}
		}

		if base == nil {
			out = append(out, inc) // 新增子记录：原样（后续清 PK 走 CREATE）
			continue
		}
		out = append(out, mergeRecordOverBase(base, inc))
	}
	return out, nil
}

// recordIdentityPK 读取记录的主键值（空串表示缺省）。
func recordIdentityPK(m map[string]any, pkField string) string {
	if m == nil {
		return ""
	}
	v, _ := readPKValue(m, pkField)
	return scalarToString(v)
}

// recordIdentityCode 读取记录的业务 code 值（空串表示缺省）。
func recordIdentityCode(m map[string]any, codeField string) string {
	if m == nil || codeField == "" {
		return ""
	}
	v, ok := getByPath(m, codeField)
	if !ok {
		return ""
	}
	return scalarToString(v)
}

// mergeRecordOverBase 以 base（旧记录）为基底，用 inc（请求字段）逐键覆盖。
//
// 覆盖是**逐键**语义：inc 里出现的键（即使值为 nil / ""）一律覆盖 base 的同名键
// —— 这正是「显式置空」的表达；inc 里没有的键保留 base 的值。
//
// 返回新 map（不改动 base 与 inc）：base 是 DB 拉回的快照，inc 是请求 map 句柄
// （钩子可能仍在用），都不应被就地改写。
func mergeRecordOverBase(base, inc map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(inc))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range inc {
		out[k] = v
	}
	return out
}
