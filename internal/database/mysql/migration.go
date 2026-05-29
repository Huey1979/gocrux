package mysql

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/Huey1979/gocrux/internal/config"

	errs "github.com/Huey1979/gocrux/errors"
	"github.com/sirupsen/logrus"
)

// Migrate 自动迁移表结构
// models 由外部注入，使用者需要传入自己的 GORM 模型定义。
func Migrate(models ...any) error {
	if len(models) == 0 {
		logrus.Info("无模型需要迁移，跳过")
		return nil
	}

	// 1. 执行 AutoMigrate（创建不存在的表和列）
	if err := DB.InternalDB().AutoMigrate(models...); err != nil {
		return errs.ErrAutoMigrate(err)
	}

	// 2. 检测并尝试自动修复所有问题
	failed := detectAndFixAllIssues(models)
	if len(failed) > 0 {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf(
			"\n========== 表结构验证不通过（共 %d 项无法自动修复）==========\n\n", len(failed)))
		for i, f := range failed {
			sb.WriteString(fmt.Sprintf("【问题 %d】%s\n", i+1, f))
		}
		sb.WriteString("====================================================\n")
		sb.WriteString("请手动修复上述问题后重新启动服务。\n")
		return fmt.Errorf("%s", sb.String())
	}

	return nil
}

// dbColInfo 数据库列信息
type dbColInfo struct {
	dataType string
	colType  string
}

// modelColInfo 模型列信息
type modelColInfo struct {
	goType string
	colTag string
	size   int
}

// detectAndFixAllIssues 检测所有表并尝试 ALTER TABLE 修复
func detectAndFixAllIssues(models []any) []string {
	var failed []string

	for _, model := range models {
		if !DB.InternalDB().Migrator().HasTable(model) {
			continue
		}

		tableName := getTableName(model)
		if tableName == "" {
			continue
		}

		dbCols := getDBColumnInfo(tableName)
		if dbCols == nil {
			failed = append(failed, fmt.Sprintf("表 %s: 无法读取 information_schema.columns", tableName))
			continue
		}

		modelCols := getModelColumnInfo(model)

		for _, mc := range modelCols {
			wrongName := correctToWrongULIDName(mc.colTag)
			wrongCol, wrongExists := dbCols[wrongName]
			_, correctExists := dbCols[mc.colTag]

			if wrongExists && !correctExists {
				sql := fmt.Sprintf("ALTER TABLE `%s` RENAME COLUMN `%s` TO `%s`", tableName, wrongName, mc.colTag)
				if err := DB.InternalDB().Exec(sql).Error; err != nil {
					failed = append(failed, fmt.Sprintf(
						"表 %s: ULID 列名不一致\n  数据库中列名: %s (%s)\n  模型期望列名: %s\n  尝试执行: %s\n  失败原因: %v",
						tableName, wrongName, wrongCol.colType, mc.colTag, sql, err))
				} else {
					logrus.Infof("✓ 已修复: %s.%s → %s", tableName, wrongName, mc.colTag)
					dbCols[mc.colTag] = wrongCol
					delete(dbCols, wrongName)
				}
			}

			dbCol, dbExists := dbCols[mc.colTag]
			if !dbExists {
				continue
			}

			expectedSQL := goTypeToMySQL(mc.goType, mc.size)
			if hasTypeConflict(dbCol.dataType, dbCol.colType, mc.goType) {
				sql := fmt.Sprintf("ALTER TABLE `%s` MODIFY COLUMN `%s` %s",
					tableName, mc.colTag, expectedSQL)
				if err := DB.InternalDB().Exec(sql).Error; err != nil {
					failed = append(failed, fmt.Sprintf(
						"表 %s.%s: 类型不兼容\n  数据库类型: %s (%s)\n  模型期望:   %s (Go %s)\n  尝试执行: %s\n  失败原因: %v",
						tableName, mc.colTag, dbCol.dataType, dbCol.colType,
						expectedSQL, mc.goType, sql, err))
				} else {
					logrus.Infof("✓ 已修复: %s.%s 类型 %s → %s",
						tableName, mc.colTag, dbCol.dataType, expectedSQL)
				}
			}
		}
	}

	return failed
}

func getDBColumnInfo(tableName string) map[string]dbColInfo {
	sqlDB, err := DB.InternalDB().DB()
	if err != nil {
		return nil
	}

	rows, err := sqlDB.Query(
		"SELECT COLUMN_NAME, DATA_TYPE, COLUMN_TYPE FROM information_schema.columns "+
			"WHERE table_schema = ? AND table_name = ?",
		config.Cfg.MySQL.Database, tableName)
	if err != nil {
		return nil
	}
	defer rows.Close()

	result := make(map[string]dbColInfo)
	for rows.Next() {
		var colName, dataType, colType string
		if err := rows.Scan(&colName, &dataType, &colType); err != nil {
			continue
		}
		result[colName] = dbColInfo{dataType: dataType, colType: colType}
	}
	return result
}

func getModelColumnInfo(model interface{}) []modelColInfo {
	t := reflect.TypeOf(model)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}

	var result []modelColInfo
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			if f.Type.Kind() == reflect.Struct {
				result = append(result, getModelColumnInfo(reflect.New(f.Type).Interface())...)
			}
			continue
		}

		colTag := extractColumnTag(f.Tag.Get("gorm"))
		if colTag == "" {
			continue
		}

		size := extractSizeTag(f.Tag.Get("gorm"))
		goType := resolveGoType(f.Type)

		result = append(result, modelColInfo{
			goType: goType,
			colTag: colTag,
			size:   size,
		})
	}
	return result
}

func extractColumnTag(gormTag string) string {
	parts := strings.Split(gormTag, ";")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "column:") {
			return strings.TrimPrefix(p, "column:")
		}
	}
	return ""
}

func extractSizeTag(gormTag string) int {
	parts := strings.Split(gormTag, ";")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "size:") {
			var v int
			fmt.Sscanf(strings.TrimPrefix(p, "size:"), "%d", &v)
			return v
		}
	}
	return 0
}

func resolveGoType(t reflect.Type) string {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "int"
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "uint"
	case reflect.Float32, reflect.Float64:
		return "float"
	case reflect.Bool:
		return "bool"
	case reflect.Struct:
		return "struct"
	default:
		return t.Kind().String()
	}
}

func goTypeToMySQL(goType string, size int) string {
	switch goType {
	case "string":
		if size > 0 {
			return fmt.Sprintf("VARCHAR(%d)", size)
		}
		return "VARCHAR(255)"
	case "int":
		return "INT"
	case "uint":
		return "INT UNSIGNED"
	case "float":
		return "DOUBLE"
	case "bool":
		return "TINYINT(1)"
	case "struct":
		return "DATETIME"
	default:
		return "VARCHAR(255)"
	}
}

func hasTypeConflict(dbDataType, dbColType, goType string) bool {
	dbLower := strings.ToLower(dbDataType)
	switch goType {
	case "string":
		if containsAny(dbLower, "varchar", "char", "text", "json", "enum", "longtext", "mediumtext", "tinytext") {
			return false
		}
	case "int", "uint":
		if containsAny(dbLower, "int", "integer") {
			return false
		}
	case "float":
		if containsAny(dbLower, "float", "double", "decimal") {
			return false
		}
	case "bool":
		if containsAny(dbLower, "tinyint", "bit") {
			return false
		}
	case "struct":
		if containsAny(dbLower, "datetime", "timestamp", "date", "time") {
			return false
		}
	}
	return true
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func getTableName(model interface{}) string {
	t := reflect.TypeOf(model)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return ""
	}
	name := t.Name()
	var result []rune
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			result = append(result, '_')
		}
		result = append(result, r)
	}
	return strings.ToLower(string(result))
}

func correctToWrongULIDName(correct string) string {
	return strings.Replace(correct, "_ulid", "_ul_id", 1)
}
