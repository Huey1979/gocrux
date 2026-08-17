package mysql

import (
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
)

// ============================================================
// Schema 修复
// ============================================================

// fixMissingTable 建表（预期有、实际无）。
func fixMissingTable(t tableSchema) error {
	sql := buildCreateTableSQL(t)
	logrus.Debugf("[migration] 建表 %s:\n%s", t.Name, sql)
	return DB.InternalDB().Exec(sql).Error
}

// buildCreateTableSQL 自写 CREATE TABLE SQL。
func buildCreateTableSQL(t tableSchema) string {
	var lines []string

	// 列定义
	for _, col := range t.Columns {
		lines = append(lines, "  "+buildColumnDef(col))
	}

	// 主键
	var pkCols []string
	for _, col := range t.Columns {
		if col.IsPrimaryKey {
			pkCols = append(pkCols, "`"+col.Name+"`")
		}
	}
	if len(pkCols) > 0 {
		lines = append(lines, "  PRIMARY KEY ("+strings.Join(pkCols, ", ")+")")
	}

	// 索引
	for _, idx := range t.Indexes {
		cols := make([]string, len(idx.Columns))
		for i, c := range idx.Columns {
			cols[i] = "`" + c + "`"
		}
		colList := strings.Join(cols, ", ")
		if idx.Unique {
			lines = append(lines, fmt.Sprintf("  UNIQUE KEY `%s` (%s)", idx.Name, colList))
		} else {
			lines = append(lines, fmt.Sprintf("  INDEX `%s` (%s)", idx.Name, colList))
		}
	}

	return fmt.Sprintf("CREATE TABLE `%s` (\n%s\n)", t.Name, strings.Join(lines, ",\n"))
}

// buildColumnDef 构建单列定义 SQL 片段。
func buildColumnDef(col columnSchema) string {
	def := "`" + col.Name + "` " + col.DataType

	if col.Size > 0 && needsSize(col.DataType) {
		def += fmt.Sprintf("(%d)", col.Size)
	}

	// 主键字段强制 NOT NULL
	if !col.Nullable || col.IsPrimaryKey {
		def += " NOT NULL"
	} else {
		def += " NULL"
	}

	if col.HasDefault {
		def += " DEFAULT " + quoteDefault(col.DefaultVal)
	}

	return def
}

// quoteDefault 对默认值加引号（字符串默认值需要，SQL 关键字不需要）。
func quoteDefault(val string) string {
	upper := strings.ToUpper(val)
	switch upper {
	case "CURRENT_TIMESTAMP", "NULL", "CURRENT_DATE", "CURRENT_TIME":
		return val
	case "TRUE":
		return "1"
	case "FALSE":
		return "0"
	}
	// 已是带引号的字符串（如 'active'）不需要重复加
	if strings.HasPrefix(val, "'") && strings.HasSuffix(val, "'") {
		return val
	}
	return "'" + val + "'"
}

// needsSize 判断数据类型是否需要 Size 参数。
func needsSize(dataType string) bool {
	switch strings.ToUpper(dataType) {
	case "VARCHAR", "CHAR":
		return true
	default:
		return false
	}
}

// ============================================================
// fixConflicts
// ============================================================

// fixConflicts 修复表结构不一致。
// expected 为完整的预期 tableSchema，用于空表 DROP 后直接重建。
func fixConflicts(d tableDiff, expected tableSchema) (warnings []string, err error) {
	db := DB.InternalDB()

	// 1. 检查是否空表（表不存在也走空表重建）
	var count int64
	if err := db.Raw(fmt.Sprintf("SELECT COUNT(*) FROM `%s`", d.Table)).Scan(&count).Error; err != nil {
		if strings.Contains(err.Error(), "1146") {
			// 表不存在 → 用完整预期 schema 直接建表
			logrus.Infof("[migration] 表 %s 不存在，直接建表", d.Table)
			return nil, fixMissingTable(expected)
		}
		return nil, fmt.Errorf("查询表 %s 行数失败: %w", d.Table, err)
	}

	if count == 0 {
		logrus.Infof("[migration] 表 %s 为空，DROP 后重建", d.Table)
		if err := db.Exec(fmt.Sprintf("DROP TABLE `%s`", d.Table)).Error; err != nil {
			return nil, fmt.Errorf("DROP 表 %s 失败: %w", d.Table, err)
		}
		return nil, fixMissingTable(expected)
	}

	// 2. 有数据 → 逐项修复

	// a. DROP 多余索引
	for _, idx := range d.ExtraIndexes {
		sql := fmt.Sprintf("ALTER TABLE `%s` DROP INDEX `%s`", d.Table, idx.Name)
		logrus.Infof("[migration] %s", sql)
		if err := db.Exec(sql).Error; err != nil {
			return nil, fmt.Errorf("删除索引 %s.%s 失败: %w", d.Table, idx.Name, err)
		}
	}

	// b. ADD 缺失列
	for _, col := range d.MissingColumns {
		def := buildColumnDef(col)
		sql := fmt.Sprintf("ALTER TABLE `%s` ADD COLUMN %s", d.Table, def)
		logrus.Infof("[migration] %s", sql)
		if err := db.Exec(sql).Error; err != nil {
			return nil, fmt.Errorf("添加列 %s.%s 失败: %w", d.Table, col.Name, err)
		}
	}

	// c. 多余列
	for _, col := range d.ExtraColumns {
		if col.Nullable || col.HasDefault {
			warnings = append(warnings, fmt.Sprintf("多余列: %s.%s（可空或有默认值，保留未删除）", d.Table, col.Name))
		} else {
			return nil, fmt.Errorf("多余列 %s.%s 不允许 NULL 且无默认值，无法安全保留，请手动处理", d.Table, col.Name)
		}
	}

	// d. MODIFY 类型/长度/可空/DEFAULT 不一致的列
	//    注意：MySQL MODIFY COLUMN 未指定的属性会重置为类型默认，故 buildColumnDef
	//    已含预期 DEFAULT 时写入 DEFAULT；预期无 DEFAULT 时 MODIFY 将清除残留默认值
	//    （方案 B 关键：老表 DEFAULT 1 → 无 default tag → MODIFY 后不再有 DEFAULT）。
	for _, cc := range d.ColumnConflicts {
		def := buildColumnDef(cc.Expected)
		sql := fmt.Sprintf("ALTER TABLE `%s` MODIFY COLUMN %s", d.Table, def)
		logrus.Infof("[migration] %s", sql)
		if err := db.Exec(sql).Error; err != nil {
			return nil, fmt.Errorf("修改列 %s.%s 失败: %w", d.Table, cc.Name, err)
		}
	}

	// e. ADD 缺失索引
	for _, idx := range d.MissingIndexes {
		cols := make([]string, len(idx.Columns))
		for i, c := range idx.Columns {
			cols[i] = "`" + c + "`"
		}
		colList := strings.Join(cols, ", ")
		var sql string
		if idx.Unique {
			sql = fmt.Sprintf("ALTER TABLE `%s` ADD UNIQUE KEY `%s` (%s)", d.Table, idx.Name, colList)
		} else {
			sql = fmt.Sprintf("ALTER TABLE `%s` ADD INDEX `%s` (%s)", d.Table, idx.Name, colList)
		}
		logrus.Infof("[migration] %s", sql)
		if err := db.Exec(sql).Error; err != nil {
			if idx.Unique {
				return nil, fmt.Errorf("添加唯一索引 %s.%s 失败（可能存在重复数据）: %w", d.Table, idx.Name, err)
			}
			warnings = append(warnings, fmt.Sprintf("添加索引 %s.%s 失败: %v", d.Table, idx.Name, err))
		}
	}

	return warnings, nil
}
