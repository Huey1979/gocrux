package mysql

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"gorm.io/gorm/schema"
)

// ============================================================
// Schema 读取
// ============================================================

var namingStrategy = schema.NamingStrategy{}

// readEntitySchemas 从 entity struct 反射解析所有预期表结构。
func readEntitySchemas(models []any) ([]tableSchema, error) {
	var tables []tableSchema
	for _, m := range models {
		t, err := parseEntitySchema(m)
		if err != nil {
			return nil, fmt.Errorf("解析 entity %T 失败: %w", m, err)
		}
		tables = append(tables, t)
	}
	return tables, nil
}

// parseEntitySchema 反射解析单个 entity。
func parseEntitySchema(model any) (tableSchema, error) {
	t := reflect.TypeOf(model)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return tableSchema{}, fmt.Errorf("%s 不是 struct 类型", t.Name())
	}

	// 表名：通过 TableName() 接口获取
	tableName := getTableName(model)

	tbl := tableSchema{Name: tableName}
	parseStructFields(t, &tbl, model)
	mergeUniqueIndexes(&tbl)
	return tbl, nil
}

// getTableName 通过 entity 的 TableName() 方法获取表名，默认蛇形命名。
func getTableName(model any) string {
	if tn, ok := model.(interface{ TableName() string }); ok {
		return tn.TableName()
	}
	t := reflect.TypeOf(model)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return namingStrategy.TableName(t.Name())
}

// parseStructFields 递归解析 struct 字段（含嵌入匿名 struct）。
func parseStructFields(t reflect.Type, tbl *tableSchema, model any) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		// 嵌入匿名 struct → 递归展开
		if field.Anonymous {
			parseStructFields(field.Type, tbl, model)
			continue
		}

		// gorm:"-" 显式排除的字段跳过
		gormTag := field.Tag.Get("gorm")
		if gormTag == "-" {
			continue
		}

		// 列名：优先 gorm column tag，其次 json tag
		colName := getColumnName(field, gormTag)
		if colName == "" || colName == "-" {
			continue
		}

		// 列定义（gormTag 为空时仅靠 Go 类型推断）
		col := parseColumnSchema(colName, field, gormTag)
		tbl.Columns = append(tbl.Columns, col)

		// 索引
		idxs := parseIndexes(tbl.Name, colName, gormTag)
		tbl.Indexes = append(tbl.Indexes, idxs...)
	}
}

// getColumnName 获取列名：优先 column tag → json tag → 蛇形命名。
func getColumnName(field reflect.StructField, gormTag string) string {
	for _, seg := range splitTag(gormTag) {
		if strings.HasPrefix(seg, "column:") {
			return strings.TrimPrefix(seg, "column:")
		}
	}
	// 无 column tag → json tag（去掉 ,omitempty 等后缀）
	if jsonTag := field.Tag.Get("json"); jsonTag != "" {
		name, _, _ := strings.Cut(jsonTag, ",")
		if name != "" && name != "-" {
			return name
		}
	}
	// 都无 → 蛇形命名
	return namingStrategy.ColumnName("", field.Name)
}

// parseColumnSchema 解析 GORM tag → columnSchema。
func parseColumnSchema(name string, field reflect.StructField, gormTag string) columnSchema {
	col := columnSchema{
		Name:     name,
		Nullable: true, // 默认允许 NULL
	}

	segments := splitTag(gormTag)

	for _, seg := range segments {
		switch {
		case seg == "primaryKey":
			col.IsPrimaryKey = true
			col.Nullable = false // 主键强制 NOT NULL
		case seg == "not null":
			col.Nullable = false
		case strings.HasPrefix(seg, "size:"):
			v := strings.TrimPrefix(seg, "size:")
			col.Size, _ = strconv.Atoi(v)
		case strings.HasPrefix(seg, "default:"):
			v := strings.TrimPrefix(seg, "default:")
			// 方案 B：default 只允许 0 值/无。default:(-)（gentity 对非零默认
			// 值的产物）或无值 → 视为无显式默认值，不参与 DEFAULT 比对/同步。
			if v == "(-)" || v == "" {
				// 无默认值
			} else {
				col.HasDefault = true
				col.DefaultVal = v
			}
		case strings.HasPrefix(seg, "type:"):
			col.DataType = strings.ToUpper(strings.TrimPrefix(seg, "type:"))
		}
	}

	// 未显式指定类型 → Go 类型推断
	if col.DataType == "" {
		col.DataType, col.Size = inferSQLType(field, gormTag, col.Size)
	}

	return col
}

// inferSQLType 根据 Go 类型推断 SQL 类型和默认 Size。
func inferSQLType(field reflect.StructField, gormTag string, explicitSize int) (dataType string, size int) {
	size = explicitSize
	ft := field.Type

	switch ft.Kind() {
	case reflect.String:
		if size == 0 {
			size = 255
		}
		return "VARCHAR", size
	case reflect.Int, reflect.Int32:
		return "INT", 0
	case reflect.Int8:
		return "TINYINT", 0
	case reflect.Int64:
		return "BIGINT", 0
	case reflect.Float64, reflect.Float32:
		return "DOUBLE", 0
	case reflect.Bool:
		return "TINYINT", 1
	case reflect.Struct:
		if ft.String() == "time.Time" {
			return "DATETIME", 0
		}
	case reflect.Ptr:
		if ft.Elem().Kind() == reflect.Struct && ft.Elem().String() == "time.Time" {
			return "DATETIME", 0
		}
	}
	// 兜底
	if size == 0 {
		size = 255
	}
	return "VARCHAR", size
}

// parseIndexes 解析 GORM tag 中的 index / uniqueIndex → []indexSchema。
func parseIndexes(tableName, colName, gormTag string) []indexSchema {
	var indexes []indexSchema
	segments := splitTag(gormTag)

	for _, seg := range segments {
		switch {
		case seg == "index":
			indexes = append(indexes, indexSchema{
				Name:    fmt.Sprintf("idx_%s_%s", tableName, colName),
				Columns: []string{colName},
				Unique:  false,
			})
		case seg == "uniqueIndex":
			indexes = append(indexes, indexSchema{
				Name:    fmt.Sprintf("uni_%s_%s", tableName, colName),
				Columns: []string{colName},
				Unique:  true,
			})
		case strings.HasPrefix(seg, "uniqueIndex:"):
			idxName := strings.TrimPrefix(seg, "uniqueIndex:")
			indexes = append(indexes, indexSchema{
				Name:    idxName,
				Columns: []string{colName},
				Unique:  true,
			})
		}
	}
	return indexes
}

// mergeUniqueIndexes 合并同名的 uniqueIndex → 多列联合唯一索引。
func mergeUniqueIndexes(tbl *tableSchema) {
	idxMap := make(map[string]*indexSchema)
	ordered := make([]string, 0)

	for i := range tbl.Indexes {
		idx := &tbl.Indexes[i]
		if !idx.Unique {
			ordered = append(ordered, idx.Name)
			idxMap[idx.Name] = idx
			continue
		}

		existing, ok := idxMap[idx.Name]
		if ok {
			existing.Columns = append(existing.Columns, idx.Columns...)
		} else {
			ordered = append(ordered, idx.Name)
			idxMap[idx.Name] = idx
		}
	}

	merged := make([]indexSchema, 0, len(ordered))
	for _, name := range ordered {
		merged = append(merged, *idxMap[name])
	}
	tbl.Indexes = merged
}

// splitTag 按 ; 切分 tag，忽略空白段。
func splitTag(tag string) []string {
	parts := strings.Split(tag, ";")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// ============================================================
// readActualSchemas — 从 information_schema 读取实际结构
// ============================================================

// readActualSchemas 从 information_schema 读取数据库实际表结构。
func readActualSchemas(expected []tableSchema) ([]tableSchema, error) {
	db := DB.InternalDB()
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	if len(expected) == 0 {
		return nil, nil
	}

	tableNames := make([]string, len(expected))
	for i, e := range expected {
		tableNames[i] = e.Name
	}

	// 按 Name 建索引
	tableMap := make(map[string]*tableSchema, len(expected))
	for _, e := range expected {
		tableMap[e.Name] = &tableSchema{Name: e.Name}
	}

	// ---- 1. 读取列信息 ----
	type colRow struct {
		TableName     string `gorm:"column:TABLE_NAME"`
		ColumnName    string `gorm:"column:COLUMN_NAME"`
		DataType      string `gorm:"column:DATA_TYPE"`
		CharMaxLen    *int64 `gorm:"column:CHARACTER_MAXIMUM_LENGTH"`
		IsNullable    string `gorm:"column:IS_NULLABLE"`
		ColumnDefault *string `gorm:"column:COLUMN_DEFAULT"`
		ColumnKey     string `gorm:"column:COLUMN_KEY"`
	}
	var colRows []colRow
	if err := db.Raw(
		`SELECT TABLE_NAME, COLUMN_NAME, UPPER(DATA_TYPE) AS DATA_TYPE,
		        CHARACTER_MAXIMUM_LENGTH, IS_NULLABLE, COLUMN_DEFAULT, COLUMN_KEY
		 FROM information_schema.COLUMNS
		 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN ?`, tableNames,
	).Scan(&colRows).Error; err != nil {
		return nil, fmt.Errorf("查询 information_schema.COLUMNS 失败: %w", err)
	}

	for _, r := range colRows {
		ts, ok := tableMap[r.TableName]
		if !ok {
			continue
		}
		col := columnSchema{
			Name:         r.ColumnName,
			DataType:     r.DataType,
			Nullable:     strings.ToUpper(r.IsNullable) == "YES",
			IsPrimaryKey: r.ColumnKey == "PRI",
		}
		if r.CharMaxLen != nil {
			col.Size = int(*r.CharMaxLen)
		}
		if r.ColumnDefault != nil {
			col.HasDefault = true
			col.DefaultVal = *r.ColumnDefault
		}
		ts.Columns = append(ts.Columns, col)
	}

	// ---- 2. 读取索引信息 ----
	type idxRow struct {
		TableName  string `gorm:"column:TABLE_NAME"`
		IndexName  string `gorm:"column:INDEX_NAME"`
		ColumnName string `gorm:"column:COLUMN_NAME"`
		NonUnique  int    `gorm:"column:NON_UNIQUE"`
		SeqInIndex int    `gorm:"column:SEQ_IN_INDEX"`
	}
	var idxRows []idxRow
	if err := db.Raw(
		`SELECT TABLE_NAME, INDEX_NAME, COLUMN_NAME, NON_UNIQUE, SEQ_IN_INDEX
		 FROM information_schema.STATISTICS
		 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN ?
		 ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`, tableNames,
	).Scan(&idxRows).Error; err != nil {
		return nil, fmt.Errorf("查询 information_schema.STATISTICS 失败: %w", err)
	}

	// 聚合索引列
	type idxGroup struct {
		ts *tableSchema
		m  map[string]*indexSchema
	}
	tableIdxGroups := make(map[string]*idxGroup, len(expected))
	for _, e := range expected {
		tableIdxGroups[e.Name] = &idxGroup{
			ts: tableMap[e.Name],
			m:  make(map[string]*indexSchema),
		}
	}

	for _, r := range idxRows {
		if r.IndexName == "PRIMARY" {
			continue
		}
		g, ok := tableIdxGroups[r.TableName]
		if !ok {
			continue
		}
		idx, exists := g.m[r.IndexName]
		if !exists {
			idx = &indexSchema{
				Name:    r.IndexName,
				Unique:  r.NonUnique == 0,
				Columns: make([]string, 0),
			}
			g.m[r.IndexName] = idx
		}
		idx.Columns = append(idx.Columns, r.ColumnName)
	}

	for _, g := range tableIdxGroups {
		for _, idx := range g.m {
			g.ts.Indexes = append(g.ts.Indexes, *idx)
		}
	}

	// 仅返回 DB 中实际存在的表（空表不放入结果，使其进入 missingTables）
	result := make([]tableSchema, 0, len(expected))
	for _, e := range expected {
		ts := tableMap[e.Name]
		if len(ts.Columns) == 0 {
			continue // 表在 DB 中不存在
		}
		result = append(result, *ts)
	}
	return result, nil
}

// getAllDBTableNames 查询 MySQL 中当前数据库的所有表名（不受 entity 列表限制）。
func getAllDBTableNames() ([]string, error) {
	db := DB.InternalDB()
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	type tableRow struct {
		TableName string `gorm:"column:TABLE_NAME"`
	}
	var rows []tableRow
	if err := db.Raw(
		`SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'`,
	).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询 information_schema.TABLES 失败: %w", err)
	}

	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.TableName)
	}
	return names, nil
}
