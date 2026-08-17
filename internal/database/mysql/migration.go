package mysql

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"
)

// ============================================================
// Migrate — 纯 SQL 数据库迁移（不依赖 GORM AutoMigrate）
// ============================================================

// Migrate 根据 models（entity 定义）管理数据库 schema。
// models 由外部注入，使用者需要传入自己的 GORM 模型定义（entity 指针或值）。
//
// 流程：
//  1. 从 entity struct 解析预期 schema（列 + 索引 + 默认值）
//  2. 从 information_schema 读取实际 schema
//  3. 纠正历史 ULID 列名（_ul_id → _ulid，GORM AutoMigrate 时代的产物）
//  4. 比对，找出差异
//  5. 建缺失的表（自写 CREATE TABLE）
//  6. 记录多余的表（仅警告，不删除）
//  7. 修复不一致的表（按有数据/无数据分别处理）
//  8. 输出警告汇总
//
// 本实现替代旧的 GORM AutoMigrate 方案（规避其 1091 重试、索引/列改名不可预测等
// 历史问题），参考 heims 项目内部自写的 migration 移植为公共组件。
func Migrate(models ...any) error {
	if len(models) == 0 {
		logrus.Info("无模型需要迁移，跳过")
		return nil
	}

	// 1. 读取预期 schema
	expected, err := readEntitySchemas(models)
	if err != nil {
		return fmt.Errorf("读取 entity schema 失败: %w", err)
	}

	// 2. 读取实际 schema（entity 定义的表）
	actual, err := readActualSchemas(expected)
	if err != nil {
		return fmt.Errorf("读取数据库 schema 失败: %w", err)
	}

	// 2.5 查询 MySQL 中所有表（不受 entity 列表限制），用于检测多余的表
	allDBTables, err := getAllDBTableNames()
	if err != nil {
		return fmt.Errorf("读取数据库全部表名失败: %w", err)
	}

	// 2.6 纠正历史 ULID 列名（_ul_id → _ulid）
	actual, err = fixULIDColumnNames(expected, actual)
	if err != nil {
		return fmt.Errorf("纠正 ULID 列名失败: %w", err)
	}

	// 3. 比对 + 输出诊断日志
	missingTables, extraTables, diffs := compareSchemas(expected, actual)

	// 额外检测：MySQL 中存在但 entity 未定义的表
	extraFromDB := findExtraTables(allDBTables, expected)
	extraTables = append(extraTables, extraFromDB...)

	logrus.Infof("[migration] 预期 %d 表 | MySQL 实际 %d 表 | 缺失 %d | 多余 %d | 差异 %d",
		len(expected), len(allDBTables), len(missingTables), len(extraTables), len(diffs))
	for _, t := range missingTables {
		logrus.Infof("[migration]   缺失: %s", t.Name)
	}
	for _, t := range extraTables {
		logrus.Warnf("[migration]   多余: %s（保留未删除）", t.Name)
	}
	for _, d := range diffs {
		logrus.Infof("[migration]   差异: %s（缺列%d 多列%d 冲突%d 缺索引%d 多索引%d）",
			d.Table, len(d.MissingColumns), len(d.ExtraColumns), len(d.ColumnConflicts),
			len(d.MissingIndexes), len(d.ExtraIndexes))
	}

	// 4. 建缺失的表
	for _, t := range missingTables {
		if err := fixMissingTable(t); err != nil {
			return fmt.Errorf("建表 %s 失败: %w", t.Name, err)
		}
	}

	// 5. 记录多余的表（仅警告）
	var warnings []string
	for _, t := range extraTables {
		warnings = append(warnings, fmt.Sprintf("多余的表: %s（未在 entity 中定义，保留未删除）", t.Name))
	}

	// 6. 修复不一致的表（传入完整预期 schema，空表 DROP 后直接重建）
	expMap := make(map[string]tableSchema, len(expected))
	for _, e := range expected {
		expMap[e.Name] = e
	}
	for _, d := range diffs {
		exp, ok := expMap[d.Table]
		if !ok {
			return fmt.Errorf("内部错误: 预期 schema 中找不到表 %s", d.Table)
		}
		w, err := fixConflicts(d, exp)
		if err != nil {
			return fmt.Errorf("修复表 %s 失败: %w", d.Table, err)
		}
		warnings = append(warnings, w...)
	}

	// 7. 输出汇总
	if len(warnings) > 0 {
		sort.Strings(warnings)
		logrus.Warnf("\n========== Schema 迁移警告（共 %d 项）==========\n%s\n================================================",
			len(warnings), strings.Join(warnings, "\n"))
	}

	return nil
}

// fixULIDColumnNames 纠正历史上 GORM AutoMigrate 产生的 ULID 列名错误（_ul_id → _ulid）。
// 对每张实际表：若实际列名为 xxx_ul_id 而预期列名为 xxx_ulid，执行
// ALTER TABLE ... RENAME COLUMN，并在内存中同步更新列名与索引列名。
// 返回纠正后的实际 schema（调用方后续比对使用）。
func fixULIDColumnNames(expected, actual []tableSchema) ([]tableSchema, error) {
	// 预期列名索引：表名 → 列名集合
	expCols := make(map[string]map[string]bool, len(expected))
	for _, t := range expected {
		cols := make(map[string]bool, len(t.Columns))
		for _, c := range t.Columns {
			cols[c.Name] = true
		}
		expCols[t.Name] = cols
	}

	for i := range actual {
		expSet, ok := expCols[actual[i].Name]
		if !ok {
			continue
		}
		for j := range actual[i].Columns {
			col := &actual[i].Columns[j]
			if !strings.HasSuffix(col.Name, "_ul_id") {
				continue
			}
			correct := strings.TrimSuffix(col.Name, "_ul_id") + "_ulid"
			if !expSet[correct] {
				continue
			}
			sql := fmt.Sprintf("ALTER TABLE `%s` RENAME COLUMN `%s` TO `%s`", actual[i].Name, col.Name, correct)
			if err := DB.InternalDB().Exec(sql).Error; err != nil {
				return nil, fmt.Errorf("重命名列 %s.%s → %s 失败: %w", actual[i].Name, col.Name, correct, err)
			}
			logrus.Infof("✓ 已修复: %s.%s → %s", actual[i].Name, col.Name, correct)
			col.Name = correct
		}
	}

	// 同步索引中的列名（若索引引用了被改名的列）
	for i := range actual {
		for j := range actual[i].Indexes {
			for k, c := range actual[i].Indexes[j].Columns {
				if strings.HasSuffix(c, "_ul_id") {
					actual[i].Indexes[j].Columns[k] = strings.TrimSuffix(c, "_ul_id") + "_ulid"
				}
			}
		}
	}

	return actual, nil
}
