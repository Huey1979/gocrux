package main

import (
	"database/sql"
	"fmt"
	"strings"
)

// ColumnInfo 数据库列信息
type ColumnInfo struct {
	Name       string
	Type       string // 原始类型，如 varchar(26)
	IsNullable bool
	Default    sql.NullString
	Key        string // PRI / UNI / MUL
	Extra      string // auto_increment
	Comment    string
}

// TableInfo 表信息
type TableInfo struct {
	Name    string
	Comment string
	Columns []ColumnInfo
}

// scanTable 扫描单表结构
func scanTable(db *sql.DB, schemaName, tableName string) (*TableInfo, error) {
	columns, err := scanColumns(db, schemaName, tableName)
	if err != nil {
		return nil, err
	}

	comment, err := getTableComment(db, schemaName, tableName)
	if err != nil {
		return nil, err
	}

	return &TableInfo{
		Name:    tableName,
		Comment: comment,
		Columns: columns,
	}, nil
}

// scanAllTables 扫描库中所有表
func scanAllTables(db *sql.DB, schemaName string) ([]string, error) {
	rows, err := db.Query(`
		SELECT TABLE_NAME
		FROM INFORMATION_SCHEMA.TABLES
		WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'BASE TABLE'
		ORDER BY TABLE_NAME
	`, schemaName)
	if err != nil {
		return nil, fmt.Errorf("查询表列表失败: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

// scanColumns 扫描列信息
func scanColumns(db *sql.DB, schemaName, tableName string) ([]ColumnInfo, error) {
	rows, err := db.Query(`
		SELECT
			COLUMN_NAME,
			COLUMN_TYPE,
			IS_NULLABLE,
			COLUMN_DEFAULT,
			COLUMN_KEY,
			EXTRA,
			COLUMN_COMMENT
		FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY ORDINAL_POSITION
	`, schemaName, tableName)
	if err != nil {
		return nil, fmt.Errorf("查询列信息失败: %w", err)
	}
	defer rows.Close()

	var columns []ColumnInfo
	for rows.Next() {
		var col ColumnInfo
		var isNullable string
		if err := rows.Scan(&col.Name, &col.Type, &isNullable, &col.Default, &col.Key, &col.Extra, &col.Comment); err != nil {
			return nil, err
		}
		col.IsNullable = strings.ToUpper(isNullable) == "YES"
		columns = append(columns, col)
	}
	return columns, rows.Err()
}

// getTableComment 获取表注释
func getTableComment(db *sql.DB, schemaName, tableName string) (string, error) {
	var comment string
	err := db.QueryRow(`
		SELECT TABLE_COMMENT
		FROM INFORMATION_SCHEMA.TABLES
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
	`, schemaName, tableName).Scan(&comment)
	if err != nil {
		return "", err
	}
	return comment, nil
}
