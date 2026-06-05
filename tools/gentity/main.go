package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/go-sql-driver/mysql"
)

func main() {
	dsn := flag.String("dsn", "", "MySQL DSN, 如 root:pass@tcp(localhost:3306)/dbname?charset=utf8mb4&parseTime=true")
	table := flag.String("table", "", "指定表名（单表生成）")
	all := flag.Bool("all", false, "生成库中所有表")
	outDir := flag.String("out", "generated", "输出目录")

	// 新增：字段检查 & 配置
	check := flag.Bool("check", false, "检查所有表的必填字段，生成 ALTER TABLE SQL（不生成 entity/blueprint）")
	fieldConfig := flag.String("field-config", "", "框架字段映射 YAML 配置文件路径")
	flag.Parse()

	if *dsn == "" {
		fmt.Println("错误: 必须指定 --dsn")
		fmt.Println("用法: go run tools/gentity --dsn 'root:pass@tcp(localhost:3306)/mydb?charset=utf8mb4&parseTime=true' [--all | --table xxx] [--check] [--field-config fields.yml]")
		os.Exit(1)
	}

	if *table == "" && !*all && !*check {
		fmt.Println("错误: 必须指定 --table、--all 或 --check")
		os.Exit(1)
	}

	// 加载字段配置
	cfg, err := LoadFieldConfig(*fieldConfig)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		os.Exit(1)
	}

	// 解析 DSN 获取库名
	schemaName := parseSchema(*dsn)
	if schemaName == "" {
		fmt.Println("错误: 无法从 DSN 解析数据库名")
		os.Exit(1)
	}

	// 连接数据库
	db, err := sql.Open("mysql", *dsn)
	if err != nil {
		fmt.Printf("连接数据库失败: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		fmt.Printf("数据库 Ping 失败: %v\n", err)
		os.Exit(1)
	}

	// ———— check 模式：检查必填字段 → 生成 ALTER TABLE SQL ————
	if *check {
		runCheckMode(db, schemaName, cfg, *outDir)
		return
	}

	// ———— 正常模式：生成 entity + blueprint ————
	// 获取表列表
	var tableNames []string
	if *all {
		tableNames, err = scanAllTables(db, schemaName)
		if err != nil {
			fmt.Printf("获取表列表失败: %v\n", err)
			os.Exit(1)
		}
	} else {
		tableNames = []string{*table}
	}

	// 确保输出目录存在
	entityDir := filepath.Join(*outDir, "entity")
	blueprintDir := filepath.Join(*outDir, "blueprint")
	os.MkdirAll(entityDir, 0755)
	os.MkdirAll(blueprintDir, 0755)

	// 生成 shared blueprint helper
	writeSharedBlueprint(blueprintDir)

	// 逐表生成
	generated := 0
	skipped := 0
	for _, tName := range tableNames {
		// 排除表跳过
		if cfg.IsExcluded(tName) {
			fmt.Printf("跳过: %s (排除列表)\n", tName)
			skipped++
			continue
		}

		fmt.Printf("扫描表: %s ... ", tName)
		ti, err := scanTable(db, schemaName, tName)
		if err != nil {
			fmt.Printf("失败: %v\n", err)
			continue
		}
		fmt.Printf("(%d 列) ", len(ti.Columns))

		// 生成实体（传入字段配置）
		entityPath := filepath.Join(entityDir, tName+".go")
		if err := os.WriteFile(entityPath, []byte(generateEntity(ti, cfg)), 0644); err != nil {
			fmt.Printf("写入实体失败: %v\n", err)
			continue
		}

		// 生成蓝图
		bpPath := filepath.Join(blueprintDir, tName+".go")
		if err := os.WriteFile(bpPath, []byte(generateBlueprint(ti)), 0644); err != nil {
			fmt.Printf("写入蓝图失败: %v\n", err)
			continue
		}

		generated++
		fmt.Println("OK")
	}

	if skipped > 0 {
		fmt.Printf("\n已跳过 %d 个排除表\n", skipped)
	}
	fmt.Printf("\n生成完成！共 %d 张表\n", generated)
	fmt.Printf("  实体文件: %s/\n", entityDir)
	fmt.Printf("  蓝图文件: %s/\n", blueprintDir)
	fmt.Println()
	fmt.Println("使用方式:")
	fmt.Println("  1. 复制 generated/entity/*.go → internal/model/entity/")
	fmt.Println("  2. 复制 generated/blueprint/*.go → internal/generated/")
	fmt.Println("  3. 在 cmd/main.go 中:")
	fmt.Println("     import bp \"yourproject/internal/generated\"")
	fmt.Println("     blues := bp.NewBlueprints(serviceReg, handlerReg)")
	fmt.Println("     blues.Users.RegisterUsers(apiGroup)")
	fmt.Println("     bootstrap.Migrate(&entity.User{})")
}

// runCheckMode 检查所有表的必填字段，生成 ALTER TABLE SQL
func runCheckMode(db *sql.DB, schemaName string, cfg *FieldConfig, outDir string) {
	// 获取所有表
	tableNames, err := scanAllTables(db, schemaName)
	if err != nil {
		fmt.Printf("获取表列表失败: %v\n", err)
		os.Exit(1)
	}

	// 逐表扫描并检查
	type tableResult struct {
		Table  string
		Alters []string
	}
	var (
		results   []tableResult
		total     int
		excluded  int
		withIssue int
	)

	for _, tName := range tableNames {
		// 排除表
		if cfg.IsExcluded(tName) {
			excluded++
			continue
		}
		total++

		ti, err := scanTable(db, schemaName, tName)
		if err != nil {
			fmt.Printf("警告: 扫描 %s 失败: %v\n", tName, err)
			continue
		}

		alters := cfg.CheckRequiredFields(ti)
		if len(alters) > 0 {
			withIssue++
			results = append(results, tableResult{Table: tName, Alters: alters})
		}
	}

	// 输出结果
	fmt.Println()
	if excluded > 0 {
		fmt.Printf("已排除 %d 个表\n", excluded)
	}
	fmt.Printf("已检查 %d 个表\n", total)

	if len(results) == 0 {
		fmt.Println("所有表字段完整，无需补充！")
		return
	}

	fmt.Printf("发现 %d 个表缺少框架字段:\n\n", withIssue)
	for _, r := range results {
		fmt.Printf("  [%s] 缺失 %d 个字段:\n", r.Table, len(r.Alters))
		for _, a := range r.Alters {
			fmt.Printf("    %s\n", a)
		}
		fmt.Println()
	}

	// 写入 SQL 文件
	os.MkdirAll(outDir, 0755)
	sqlPath := filepath.Join(outDir, "migration_add_fields.sql")

	// 按表分组写入
	altersByTable := make(map[string][]string)
	for _, r := range results {
		altersByTable[r.Table] = r.Alters
	}
	wrote, err := WriteMigrationSQL(sqlPath, altersByTable)
	if err != nil {
		fmt.Printf("写入 SQL 文件失败: %v\n", err)
		os.Exit(1)
	}
	if wrote {
		fmt.Printf("ALTER TABLE 语句已写入: %s\n", sqlPath)
	}
}

// parseSchema 从 DSN 中提取数据库名
func parseSchema(dsn string) string {
	// 格式: user:pass@tcp(host:port)/dbname?params
	idx := strings.LastIndex(dsn, "/")
	if idx < 0 {
		return ""
	}
	rest := dsn[idx+1:]
	qIdx := strings.Index(rest, "?")
	if qIdx >= 0 {
		return rest[:qIdx]
	}
	return rest
}

// writeSharedBlueprint 生成 Blueprints 共享结构体
func writeSharedBlueprint(dir string) {
	content := `package blueprints

import (
	"github.com/Huey1979/gocrux/handler"
	"github.com/Huey1979/gocrux/service"
	"github.com/gin-gonic/gin"
)

// Blueprints 蓝图管理器，持有 Service/Handler 注册表
type Blueprints struct {
	ServiceReg *service.ServiceRegistry
	HandlerReg *handler.HandlerRegistry
}

// NewBlueprints 创建蓝图管理器
func NewBlueprints(sr *service.ServiceRegistry, hr *handler.HandlerRegistry) *Blueprints {
	return &Blueprints{
		ServiceReg: sr,
		HandlerReg: hr,
	}
}

// SetupAll 一键注册所有蓝图路由
// 使用方式: blues.SetupAll(apiGroup)
func (b *Blueprints) SetupAll(r *gin.RouterGroup) {
	// 每张表会在这里自动追加注册调用
}
`
	path := filepath.Join(dir, "blueprints.go")
	os.WriteFile(path, []byte(content), 0644)
}
