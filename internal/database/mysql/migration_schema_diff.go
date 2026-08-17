package mysql

import (
	"strings"
)

// ============================================================
// Schema 比对
// ============================================================

// compareSchemas 比对预期与实际 schema，输出差异。
func compareSchemas(expected, actual []tableSchema) (missingTables, extraTables []tableSchema, diffs []tableDiff) {
	actualMap := make(map[string]tableSchema, len(actual))
	for _, a := range actual {
		actualMap[a.Name] = a
	}
	expectedMap := make(map[string]tableSchema, len(expected))
	for _, e := range expected {
		expectedMap[e.Name] = e
	}

	for _, e := range expected {
		a, exists := actualMap[e.Name]
		if !exists {
			missingTables = append(missingTables, e)
			continue
		}
		diff := compareTable(e, a)
		if diff != nil {
			diffs = append(diffs, *diff)
		}
	}

	for _, a := range actual {
		if _, exists := expectedMap[a.Name]; !exists {
			extraTables = append(extraTables, a)
		}
	}
	return
}

// findExtraTables 找出 MySQL 中存在但 entity 未定义的表（多余表）。
func findExtraTables(allDBTableNames []string, expected []tableSchema) []tableSchema {
	expMap := make(map[string]bool, len(expected))
	for _, e := range expected {
		expMap[e.Name] = true
	}

	var extras []tableSchema
	for _, name := range allDBTableNames {
		if !expMap[name] {
			extras = append(extras, tableSchema{Name: name})
		}
	}
	return extras
}

// compareTable 比对单张表，无差异返回 nil。
func compareTable(expected, actual tableSchema) *tableDiff {
	diff := &tableDiff{Table: expected.Name}

	// 列比对：按 Name 建索引
	expColMap := make(map[string]columnSchema, len(expected.Columns))
	for _, c := range expected.Columns {
		expColMap[c.Name] = c
	}
	actColMap := make(map[string]columnSchema, len(actual.Columns))
	for _, c := range actual.Columns {
		actColMap[c.Name] = c
	}

	// 预期有、实际无
	for _, c := range expected.Columns {
		if _, ok := actColMap[c.Name]; !ok {
			diff.MissingColumns = append(diff.MissingColumns, c)
		}
	}
	// 预期无、实际有
	for _, c := range actual.Columns {
		if _, ok := expColMap[c.Name]; !ok {
			diff.ExtraColumns = append(diff.ExtraColumns, c)
		}
	}
	// 双方都有，比对差异（含 DEFAULT 维度，方案 B：default 只允许 0 值/无）
	for _, exp := range expected.Columns {
		act, ok := actColMap[exp.Name]
		if !ok {
			continue
		}
		if exp.DataType != act.DataType || exp.Size != act.Size || exp.Nullable != act.Nullable || defaultsDiffer(exp, act) {
			diff.ColumnConflicts = append(diff.ColumnConflicts, columnConflict{
				Name:     exp.Name,
				Expected: exp,
				Actual:   act,
			})
		}
	}

	// 索引比对：按 Name 建索引
	expIdxMap := make(map[string]indexSchema, len(expected.Indexes))
	for _, idx := range expected.Indexes {
		expIdxMap[idx.Name] = idx
	}
	actIdxMap := make(map[string]indexSchema, len(actual.Indexes))
	for _, idx := range actual.Indexes {
		actIdxMap[idx.Name] = idx
	}

	// 预期有、实际无
	for _, idx := range expected.Indexes {
		if _, ok := actIdxMap[idx.Name]; !ok {
			diff.MissingIndexes = append(diff.MissingIndexes, idx)
		}
	}
	// 预期无、实际有
	for _, idx := range actual.Indexes {
		if _, ok := expIdxMap[idx.Name]; !ok {
			diff.ExtraIndexes = append(diff.ExtraIndexes, idx)
		}
	}

	// 无任何差异 → nil
	if len(diff.MissingColumns) == 0 &&
		len(diff.ExtraColumns) == 0 &&
		len(diff.ColumnConflicts) == 0 &&
		len(diff.MissingIndexes) == 0 &&
		len(diff.ExtraIndexes) == 0 {
		return nil
	}
	return diff
}

// defaultsDiffer 判断列的默认值定义是否不一致。
//
// 方案 B：default 只允许 0 值/无。老表残留的非零 DEFAULT（如 DEFAULT 1）必须被
// 识别为冲突并同步移除，否则即使 gorm tag 无 default，显式 0 仍会被 DB 列默认值
// 兜底成 1（BUG-045/047 根因之一）。
func defaultsDiffer(exp, act columnSchema) bool {
	if exp.HasDefault != act.HasDefault {
		return true
	}
	if exp.HasDefault {
		return normalizeDefaultVal(exp.DefaultVal) != normalizeDefaultVal(act.DefaultVal)
	}
	return false
}

// normalizeDefaultVal 规范化默认值文本用于比对：
// 预期侧来自 gorm tag（如 "1"、"CURRENT_TIMESTAMP"，字符串值可能带引号 'active'）；
// 实际侧来自 information_schema.COLUMN_DEFAULT（字符串值不带引号、数字为字符串形式）。
// 统一去引号/去空白/大写后比较。
func normalizeDefaultVal(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && ((s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"')) {
		s = s[1 : len(s)-1]
	}
	return strings.ToUpper(s)
}
