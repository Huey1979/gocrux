package main

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// ============================================================
// FieldMapping — 框架字段 → 数据库列名映射
// ============================================================

// FieldMapping 将框架约定的字段名映射到数据库实际列名。
// 键为框架标准名，值为数据库列名（留空表示使用默认名）。
type FieldMapping struct {
	IsDeleted string `yaml:"is_deleted"`
	DeletedAt string `yaml:"deleted_at"`
	CreatedAt string `yaml:"created_at"`
	CreatedBy string `yaml:"created_by"`
	UpdatedAt string `yaml:"updated_at"`
	UpdatedBy string `yaml:"updated_by"`
}

// Resolve 获取映射后的数据库列名。未配置则返回框架默认名。
func (fm FieldMapping) Resolve(frameworkName string) string {
	switch frameworkName {
	case "is_deleted":
		if fm.IsDeleted != "" {
			return fm.IsDeleted
		}
	case "deleted_at":
		if fm.DeletedAt != "" {
			return fm.DeletedAt
		}
	case "created_at":
		if fm.CreatedAt != "" {
			return fm.CreatedAt
		}
	case "created_by":
		if fm.CreatedBy != "" {
			return fm.CreatedBy
		}
	case "updated_at":
		if fm.UpdatedAt != "" {
			return fm.UpdatedAt
		}
	case "updated_by":
		if fm.UpdatedBy != "" {
			return fm.UpdatedBy
		}
	}
	return frameworkName
}

// ResolveAll 返回所有框架字段 → 数据库列名的映射表
func (fm FieldMapping) ResolveAll() map[string]string {
	return map[string]string{
		"is_deleted": fm.Resolve("is_deleted"),
		"deleted_at": fm.Resolve("deleted_at"),
		"created_at": fm.Resolve("created_at"),
		"created_by": fm.Resolve("created_by"),
		"updated_at": fm.Resolve("updated_at"),
		"updated_by": fm.Resolve("updated_by"),
	}
}

// ReverseLookup 根据数据库列名反查框架字段名。未找到返回空字符串。
// 用于判断某个 DB 列是否属于框架字段（含映射后的别名）。
func (fm FieldMapping) ReverseLookup(dbCol string) string {
	standard := map[string]string{
		"is_deleted": "is_deleted",
		"deleted_at": "deleted_at",
		"created_at": "created_at",
		"created_by": "created_by",
		"updated_at": "updated_at",
		"updated_by": "updated_by",
	}
	if _, ok := standard[dbCol]; ok {
		return dbCol
	}
	mapped := fm.ResolveAll()
	for fw, db := range mapped {
		if db == dbCol {
			return fw
		}
	}
	return ""
}

// ============================================================
// FieldConfig — YAML 配置结构
// ============================================================

// FieldConfig 从 YAML 文件加载的完整配置
type FieldConfig struct {
	FieldMapping  FieldMapping `yaml:"field_mapping"`
	ExcludeTables []string     `yaml:"exclude_tables"` // 排除的表（不检查、不生成）
}

// IsExcluded 判断表是否在排除列表中
func (cfg *FieldConfig) IsExcluded(tableName string) bool {
	for _, t := range cfg.ExcludeTables {
		if t == tableName {
			return true
		}
	}
	return false
}

// LoadFieldConfig 加载 YAML 配置文件。path 为空时返回空配置。
func LoadFieldConfig(path string) (*FieldConfig, error) {
	if path == "" {
		return &FieldConfig{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取字段配置文件 %s 失败: %w", path, err)
	}
	var cfg FieldConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析字段配置文件 %s 失败: %w", path, err)
	}
	return &cfg, nil
}

// isFrameworkColumn 判断 DB 列名是否为框架字段（含映射别名）
// 用于 goTypeName 中决定字段是否为指针类型。
func (cfg *FieldConfig) isFrameworkColumn(colName string) bool {
	if isFrameworkField(colName) {
		return true
	}
	return cfg.FieldMapping.ReverseLookup(colName) != ""
}

// ============================================================
// RequiredField — 必填字段定义
// ============================================================

// RequiredField 框架要求每个业务表必须存在的字段
type RequiredField struct {
	FrameworkName string // 框架标准名（如 is_deleted）
	MySQLType     string // MySQL 列类型（用于 ALTER TABLE）
	DefaultVal    string // 默认值（用于 ALTER TABLE）
	Comment       string // 列注释
}

// RequiredFields 返回非日志表必须存在的框架字段列表。
// 注意：这些字段由 Service 层自动填充（created_at/by 从 ctx 提取，
// updated_at/by 每次更新自动刷新），因此必须存在于数据库。
func RequiredFields() []RequiredField {
	return []RequiredField{
		{FrameworkName: "is_deleted", MySQLType: "tinyint(1)", DefaultVal: "0", Comment: "软删除标记"},
		{FrameworkName: "created_at", MySQLType: "datetime", DefaultVal: "CURRENT_TIMESTAMP", Comment: "创建时间"},
		{FrameworkName: "created_by", MySQLType: "varchar(26)", DefaultVal: "''", Comment: "创建人ULID"},
		{FrameworkName: "updated_at", MySQLType: "datetime", DefaultVal: "CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP", Comment: "更新时间"},
		{FrameworkName: "updated_by", MySQLType: "varchar(26)", DefaultVal: "''", Comment: "更新人ULID"},
	}
}

// ============================================================
// 字段检查 & ALTER TABLE 生成
// ============================================================

// CheckRequiredFields 检查单表必填字段，返回缺失字段的 ALTER TABLE 语句
func (cfg *FieldConfig) CheckRequiredFields(table *TableInfo) []string {
	if cfg.IsExcluded(table.Name) {
		return nil
	}

	var alters []string
	for _, rf := range RequiredFields() {
		dbCol := cfg.FieldMapping.Resolve(rf.FrameworkName)
		if !hasColumn(table, dbCol) {
			alters = append(alters, buildAlterSQL(table.Name, dbCol,
				rf.MySQLType, rf.DefaultVal, rf.Comment))
		}
	}
	return alters
}

// CheckAllTables 批量检查所有表，返回 表名→ALTER语句 的分组结果
func (cfg *FieldConfig) CheckAllTables(tables []*TableInfo) map[string][]string {
	result := make(map[string][]string)
	for _, t := range tables {
		alters := cfg.CheckRequiredFields(t)
		if len(alters) > 0 {
			result[t.Name] = alters
		}
	}
	return result
}

// buildAlterSQL 生成单条 ALTER TABLE ADD COLUMN 语句
func buildAlterSQL(tableName, colName, colType, defaultVal, comment string) string {
	return fmt.Sprintf("ALTER TABLE `%s` ADD COLUMN `%s` %s NOT NULL DEFAULT %s COMMENT '%s';",
		tableName, colName, colType, defaultVal, comment)
}

// WriteMigrationSQL 将 ALTER 语句写入 SQL 文件。
// 返回是否实际写入了内容（有缺失字段时为 true）。
func WriteMigrationSQL(path string, altersByTable map[string][]string) (bool, error) {
	if len(altersByTable) == 0 {
		return false, nil
	}
	var sb strings.Builder
	sb.WriteString("-- ============================================================\n")
	sb.WriteString("-- 自动生成的 ALTER TABLE 语句（补充缺失的框架字段）\n")
	sb.WriteString("-- 请在执行前检查，必要时调整字段顺序和默认值\n")
	sb.WriteString("-- ============================================================\n\n")

	for tableName, alters := range altersByTable {
		sb.WriteString(fmt.Sprintf("-- 表: %s\n", tableName))
		for _, a := range alters {
			sb.WriteString(a + "\n")
		}
		sb.WriteString("\n")
	}

	return true, os.WriteFile(path, []byte(sb.String()), 0644)
}
