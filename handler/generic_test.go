package handler

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Huey1979/gocrux/internal/config"
	"github.com/Huey1979/gocrux/internal/database/mysql"
	applogger "github.com/Huey1979/gocrux/internal/logger"
	"github.com/Huey1979/gocrux/repository"
	"github.com/Huey1979/gocrux/service"

	"gorm.io/gorm"
)

// ============================================================
// TestMain — 加载配置、初始化 MySQL、创建/清理测试表
// ============================================================

func TestMain(m *testing.M) {
	// 1. 加载 config.yaml（相对路径从 handler/ 包出发到项目根目录）
	cfg, err := config.Load("..\\config.yaml")
	if err != nil {
		// 也尝试从项目根目录加载（go test 可能在根目录运行）
		cfg, err = config.Load("config.yaml")
		if err != nil {
			fmt.Fprintf(os.Stderr, "加载配置文件失败: %v\n", err)
			os.Exit(1)
		}
	}

	// 2. 初始化日志系统（GORM logger 需要）
	if err := applogger.Init("./logs"); err != nil {
		fmt.Fprintf(os.Stderr, "日志初始化失败: %v\n", err)
		os.Exit(1)
	}

	// 3. 初始化 MySQL
	if err := mysql.Init(&cfg.MySQL); err != nil {
		fmt.Fprintf(os.Stderr, "MySQL 初始化失败: %v\n", err)
		os.Exit(1)
	}

	// 3. 自动迁移测试表
	db := mysql.DB.InternalDB()
	if err := db.AutoMigrate(&testParent{}, &testChild{}); err != nil {
		fmt.Fprintf(os.Stderr, "测试表迁移失败: %v\n", err)
		os.Exit(1)
	}

	// 4. 运行测试
	code := m.Run()

	// 5. 清理测试表
	db.Migrator().DropTable(&testParent{}, &testChild{})

	os.Exit(code)
}

// ============================================================
// 测试用实体定义
// ============================================================

// testParent 测试父实体。
type testParent struct {
	ID        uint   `gorm:"primaryKey;autoIncrement" json:"id"`
	Name      string `gorm:"size:100" json:"name"`
	IsDeleted int8   `gorm:"column:is_deleted;default:0" json:"-"`
}

func (t testParent) SetDefaults()               {}
func (t testParent) SetCreatedAt(tm time.Time)  {}
func (t testParent) SetCreatedBy(userID string) {}
func (t testParent) SetUpdatedAt(tm time.Time)  {}
func (t testParent) SetUpdatedBy(userID string) {}
func (t testParent) SetID()                     {}
func (t testParent) SupportsDraft() bool        { return false }
func (t testParent) SetDelete() bool            { t.IsDeleted = 1; return true }
func (t testParent) GetULID() string            { return fmt.Sprintf("%d", t.ID) }
func (t testParent) PKField() string            { return "id" }

// testChild 测试子实体。
type testChild struct {
	ID        uint   `gorm:"primaryKey;autoIncrement" json:"id"`
	ParentID  string `gorm:"size:100;index" json:"parent_id"` // FK 为 string，与 GetULID 返回一致
	Name      string `gorm:"size:100" json:"name"`
	IsDeleted int8   `gorm:"column:is_deleted;default:0" json:"-"`
}

func (t testChild) SetDefaults()               {}
func (t testChild) SetCreatedAt(tm time.Time)  {}
func (t testChild) SetCreatedBy(userID string) {}
func (t testChild) SetUpdatedAt(tm time.Time)  {}
func (t testChild) SetUpdatedBy(userID string) {}
func (t testChild) SetID()                     {}
func (t testChild) SupportsDraft() bool        { return false }
func (t testChild) SetDelete() bool            { t.IsDeleted = 1; return true }
func (t testChild) PKField() string            { return "id" }

// ============================================================
// 测试辅助函数
// ============================================================

// getTestDB 返回 MySQL internal DB（测试用）。
func getTestDB() *gorm.DB {
	return mysql.DB.InternalDB()
}

// newTestHandler 创建用于测试的 GenericHandler[testParent]。
func newTestHandler(cascades []CascadeRelation, handlerReg *HandlerRegistry, tc *TxCoordinator) *GenericHandler[testParent] {
	repo := repository.NewCRUDRepository[testParent]()
	svc := service.NewGenericService[testParent](repo, service.Config[testParent]{})
	h := &GenericHandler[testParent]{
		svc:     svc,
		svcName: "test_parent",
		config: HandlerConfig[testParent]{
			PathPrefix: "/test/parent",
			Cascades:   cascades,
		},
	}
	if handlerReg != nil {
		h.handlerReg = handlerReg
	}
	if tc != nil {
		h.txCoord = tc
	}
	return h
}

// testChildHandler 测试子 Handler（用于注册到 HandlerRegistry）。
func newChildHandler() *GenericHandler[testChild] {
	repo := repository.NewCRUDRepository[testChild]()
	svc := service.NewGenericService[testChild](repo, service.Config[testChild]{})
	return &GenericHandler[testChild]{
		svc:     svc,
		svcName: "test_child",
		config: HandlerConfig[testChild]{
			PathPrefix: "/test/child",
		},
	}
}

// errorChildHandler 用于模拟级联操作失败的 Handler（实现 CascadeHandler 接口）。
type errorChildHandler struct{}

func (h *errorChildHandler) DoCreate(ctx context.Context, requests []map[string]any) ([]any, error) {
	return nil, nil
}
func (h *errorChildHandler) DoDelete(ctx context.Context, ids []any) error {
	return nil
}
func (h *errorChildHandler) DoDeleteByFK(ctx context.Context, fkField string, fkValues []any) error {
	return fmt.Errorf("模拟子删除失败")
}
func (h *errorChildHandler) DoUpdate(ctx context.Context, fkField string, fkValue any, childrenData []map[string]any, parentVersioned bool) error {
	return nil
}
func (h *errorChildHandler) DoList(ctx context.Context, fkField string, fkValue any, followPublished bool) ([]map[string]any, error) {
	return nil, nil
}
func (h *errorChildHandler) DoGetByID(ctx context.Context, id any) (map[string]any, error) {
	return nil, nil
}
func (h *errorChildHandler) DoActivate(ctx context.Context, id any) error {
	return nil
}
func (h *errorChildHandler) DoListVersions(ctx context.Context, id any, code string) ([]map[string]any, error) {
	return nil, nil
}
func (h *errorChildHandler) DoEditVersion(ctx context.Context, id any, patches map[string]any) (map[string]any, error) {
	return nil, nil
}
func (h *errorChildHandler) PKField() string { return "id" }

// updateErrorChildHandler 模拟级联更新时子创建失败的 Handler。
type updateErrorChildHandler struct{}

func (h *updateErrorChildHandler) DoCreate(ctx context.Context, requests []map[string]any) ([]any, error) {
	return nil, fmt.Errorf("模拟子创建失败")
}
func (h *updateErrorChildHandler) DoDelete(ctx context.Context, ids []any) error {
	return nil
}
func (h *updateErrorChildHandler) DoDeleteByFK(ctx context.Context, fkField string, fkValues []any) error {
	return nil
}
func (h *updateErrorChildHandler) DoUpdate(ctx context.Context, fkField string, fkValue any, childrenData []map[string]any, parentVersioned bool) error {
	return fmt.Errorf("模拟子更新失败")
}
func (h *updateErrorChildHandler) DoList(ctx context.Context, fkField string, fkValue any, followPublished bool) ([]map[string]any, error) {
	return nil, nil
}
func (h *updateErrorChildHandler) DoGetByID(ctx context.Context, id any) (map[string]any, error) {
	return nil, nil
}
func (h *updateErrorChildHandler) DoActivate(ctx context.Context, id any) error {
	return nil
}
func (h *updateErrorChildHandler) DoListVersions(ctx context.Context, id any, code string) ([]map[string]any, error) {
	return nil, nil
}
func (h *updateErrorChildHandler) DoEditVersion(ctx context.Context, id any, patches map[string]any) (map[string]any, error) {
	return nil, nil
}
func (h *updateErrorChildHandler) PKField() string { return "id" }

// makeReq 创建 MapRequest 作为 CrudRequest。
func makeReq(m map[string]any) service.CrudRequest[testParent] {
	return &MapRequest[testParent]{data: m}
}

// cleanupTestTables 清理测试表数据（先子后父，避免 FK 约束冲突）。
func cleanupTestTables(db *gorm.DB) {
	db.Exec("DELETE FROM test_children")
	db.Exec("DELETE FROM test_parents")
}

// ============================================================
// 测试用例
// ============================================================

// TestDoCreate_NoCascade 无级联配置时直接创建。
func TestDoCreate_NoCascade(t *testing.T) {
	db := getTestDB()
	h := newTestHandler(nil, nil, nil)

	ctx := context.Background()
	reqs := []service.CrudRequest[testParent]{
		makeReq(map[string]any{"name": "parent1"}),
		makeReq(map[string]any{"name": "parent2"}),
	}

	results, err := h._doCreate(ctx, reqs)
	if err != nil {
		t.Fatalf("_doCreate 失败: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("期望 2 条结果, 实际 %d", len(results))
	}
	if results[0].Name != "parent1" || results[1].Name != "parent2" {
		t.Fatalf("名称不匹配: %s, %s", results[0].Name, results[1].Name)
	}
	if results[0].ID == 0 || results[1].ID == 0 {
		t.Fatal("ID 未自增")
	}

	// 验证数据库
	var count int64
	db.Model(&testParent{}).Count(&count)
	if count != 2 {
		t.Fatalf("数据库中应有 2 条记录, 实际 %d", count)
	}

	// 清理
	db.Exec("DELETE FROM test_parents")
}

// TestDoCreate_CascadeMissingTxCoord 配置了级联但缺 TxCoordinator → fallback 到直接创建。
func TestDoCreate_CascadeMissingTxCoord(t *testing.T) {
	db := getTestDB()
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnCreate: true},
	}
	h := newTestHandler(cascades, nil, nil) // 无 txCoord

	ctx := context.Background()
	reqs := []service.CrudRequest[testParent]{
		makeReq(map[string]any{"name": "p1"}),
	}

	results, err := h._doCreate(ctx, reqs)
	if err != nil {
		t.Fatalf("_doCreate 失败: %v", err)
	}
	if len(results) != 1 || results[0].Name != "p1" {
		t.Fatal("结果异常")
	}

	db.Exec("DELETE FROM test_parents")
}

// TestDoCreate_CascadeMissingHandlerReg 配置了级联但缺 HandlerRegistry → fallback。
func TestDoCreate_CascadeMissingHandlerReg(t *testing.T) {
	db := getTestDB()
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnCreate: true},
	}
	tc := NewTxCoordinator(db)
	h := newTestHandler(cascades, nil, tc) // 无 handlerReg

	ctx := context.Background()
	reqs := []service.CrudRequest[testParent]{
		makeReq(map[string]any{"name": "p1"}),
	}

	results, err := h._doCreate(ctx, reqs)
	if err != nil {
		t.Fatalf("_doCreate 失败: %v", err)
	}
	if len(results) != 1 || results[0].Name != "p1" {
		t.Fatal("结果异常")
	}

	db.Exec("DELETE FROM test_parents")
}

// TestDoCreate_CascadeOnCreateFalse OnCreate=false 不触发级联。
func TestDoCreate_CascadeOnCreateFalse(t *testing.T) {
	db := getTestDB()
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnCreate: false},
	}
	handlerReg := NewHandlerRegistry()
	handlerReg.Register("child", newChildHandler())
	tc := NewTxCoordinator(db)
	h := newTestHandler(cascades, handlerReg, tc)

	ctx := context.Background()
	reqs := []service.CrudRequest[testParent]{
		makeReq(map[string]any{"name": "p1", "children": []map[string]any{
			{"name": "c1"},
		}}),
	}

	results, err := h._doCreate(ctx, reqs)
	if err != nil {
		t.Fatalf("_doCreate 失败: %v", err)
	}
	if len(results) != 1 {
		t.Fatal("结果异常")
	}

	// 子表中不应有数据
	var childCount int64
	db.Model(&testChild{}).Count(&childCount)
	if childCount != 0 {
		t.Fatalf("期望子表 0 条, 实际 %d", childCount)
	}

	db.Exec("DELETE FROM test_parents")
	db.Exec("DELETE FROM test_children")
}

// TestDoCreate_CascadeSuccess 完整级联创建流程：父 + 子。
func TestDoCreate_CascadeSuccess(t *testing.T) {
	db := getTestDB()
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnCreate: true},
	}
	handlerReg := NewHandlerRegistry()
	handlerReg.Register("child", newChildHandler())
	tc := NewTxCoordinator(db)
	h := newTestHandler(cascades, handlerReg, tc)

	// 构造带子数据的 raw maps 并注入 ctx
	rawMaps := []map[string]any{
		{
			"name": "parent1",
			"children": []map[string]any{
				{"name": "child1"},
				{"name": "child2"},
			},
		},
		{
			"name": "parent2",
			"children": []map[string]any{
				{"name": "child3"},
			},
		},
	}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, rawMaps)

	reqs := []service.CrudRequest[testParent]{
		makeReq(rawMaps[0]),
		makeReq(rawMaps[1]),
	}

	results, err := h._doCreate(ctx, reqs)
	if err != nil {
		t.Fatalf("_doCreate 失败: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("期望 2 条父记录, 实际 %d", len(results))
	}

	// 验证父记录
	var parents []testParent
	db.Find(&parents)
	if len(parents) != 2 {
		t.Fatalf("期望 2 条父记录, 实际 %d", len(parents))
	}

	// 验证子记录
	var children []testChild
	db.Find(&children)
	if len(children) != 3 {
		t.Fatalf("期望 3 条子记录, 实际 %d", len(children))
	}

	// 验证 FK 正确注入
	parent1ID := fmt.Sprintf("%d", parents[0].ID)
	parent2ID := fmt.Sprintf("%d", parents[1].ID)

	childCountForP1 := 0
	childCountForP2 := 0
	for _, c := range children {
		if c.ParentID == parent1ID {
			childCountForP1++
		} else if c.ParentID == parent2ID {
			childCountForP2++
		}
	}
	if childCountForP1 != 2 {
		t.Fatalf("parent1 应有 2 条子记录, 实际 %d", childCountForP1)
	}
	if childCountForP2 != 1 {
		t.Fatalf("parent2 应有 1 条子记录, 实际 %d", childCountForP2)
	}

	db.Exec("DELETE FROM test_children")
	db.Exec("DELETE FROM test_parents")
}

// TestDoCreate_CascadeNoChildData rawMaps 中无子数据 → 仅创建父。
func TestDoCreate_CascadeNoChildData(t *testing.T) {
	db := getTestDB()
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnCreate: true},
	}
	handlerReg := NewHandlerRegistry()
	handlerReg.Register("child", newChildHandler())
	tc := NewTxCoordinator(db)
	h := newTestHandler(cascades, handlerReg, tc)

	// rawMaps 无 children 字段
	rawMaps := []map[string]any{
		{"name": "parent1"},
	}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, rawMaps)

	reqs := []service.CrudRequest[testParent]{
		makeReq(rawMaps[0]),
	}

	results, err := h._doCreate(ctx, reqs)
	if err != nil {
		t.Fatalf("_doCreate 失败: %v", err)
	}
	if len(results) != 1 {
		t.Fatal("父记录创建失败")
	}

	var childCount int64
	db.Model(&testChild{}).Count(&childCount)
	if childCount != 0 {
		t.Fatalf("子表应为空, 实际 %d 条", childCount)
	}

	db.Exec("DELETE FROM test_parents")
}

// TestDoCreate_CascadeNilRawMaps rawMaps 为 nil → 仅创建父。
func TestDoCreate_CascadeNilRawMaps(t *testing.T) {
	db := getTestDB()
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnCreate: true},
	}
	handlerReg := NewHandlerRegistry()
	handlerReg.Register("child", newChildHandler())
	tc := NewTxCoordinator(db)
	h := newTestHandler(cascades, handlerReg, tc)

	// ctx 中无 rawCreateMapsKey
	ctx := context.Background()

	reqs := []service.CrudRequest[testParent]{
		makeReq(map[string]any{"name": "p1"}),
	}

	results, err := h._doCreate(ctx, reqs)
	if err != nil {
		t.Fatalf("_doCreate 失败: %v", err)
	}
	if len(results) != 1 {
		t.Fatal("父记录创建失败")
	}

	db.Exec("DELETE FROM test_parents")
}

// TestDoCreate_CascadeChildHandlerNotFound 子 Handler 未注册 → 跳过级联。
func TestDoCreate_CascadeChildHandlerNotFound(t *testing.T) {
	db := getTestDB()
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnCreate: true},
	}
	handlerReg := NewHandlerRegistry() // 未注册 "child"
	tc := NewTxCoordinator(db)
	h := newTestHandler(cascades, handlerReg, tc)

	rawMaps := []map[string]any{
		{"name": "p1", "children": []map[string]any{{"name": "c1"}}},
	}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, rawMaps)

	reqs := []service.CrudRequest[testParent]{
		makeReq(rawMaps[0]),
	}

	results, err := h._doCreate(ctx, reqs)
	if err != nil {
		t.Fatalf("_doCreate 失败: %v", err)
	}
	if len(results) != 1 {
		t.Fatal("父记录创建失败")
	}

	var childCount int64
	db.Model(&testChild{}).Count(&childCount)
	if childCount != 0 {
		t.Fatalf("子表应为空, 实际 %d 条", childCount)
	}

	db.Exec("DELETE FROM test_parents")
}

// TestDoCreate_CascadeTransactionRollback 子创建失败时事务回滚，父也不会持久化。
func TestDoCreate_CascadeTransactionRollback(t *testing.T) {
	db := getTestDB()

	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnCreate: true},
	}

	// 创建一个自定义的子 Handler，其 Service 会故意失败
	childRepo := repository.NewCRUDRepository[testChild]()
	childSvc := service.NewGenericService[testChild](childRepo, service.Config[testChild]{})
	// 通过 hooks 在 DoCreate 中返回错误来模拟子创建失败
	childSvc.SetHooks(service.Hooks[testChild]{
		DoCreate: func(ctx context.Context, input []*testChild) ([]*testChild, error) {
			return nil, fmt.Errorf("模拟子创建失败")
		},
	})
	failingChild := &GenericHandler[testChild]{
		svc:     childSvc,
		svcName: "test_child",
		config: HandlerConfig[testChild]{
			PathPrefix: "/test/child",
		},
	}

	handlerReg := NewHandlerRegistry()
	handlerReg.Register("child", failingChild)
	tc := NewTxCoordinator(db)
	h := newTestHandler(cascades, handlerReg, tc)

	rawMaps := []map[string]any{
		{"name": "p1", "children": []map[string]any{{"name": "c1"}}},
	}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, rawMaps)

	reqs := []service.CrudRequest[testParent]{
		makeReq(rawMaps[0]),
	}

	results, err := h._doCreate(ctx, reqs)
	if err == nil {
		t.Fatal("期望返回错误，但成功了")
	}
	if results != nil {
		t.Fatal("期望 results 为 nil")
	}

	// 验证事务已回滚：父记录不应存在
	var parentCount int64
	db.Model(&testParent{}).Count(&parentCount)
	if parentCount != 0 {
		t.Fatalf("事务应回滚，父表应为空，实际 %d 条", parentCount)
	}

	var childCount int64
	db.Model(&testChild{}).Count(&childCount)
	if childCount != 0 {
		t.Fatalf("事务应回滚，子表应为空，实际 %d 条", childCount)
	}

	db.Exec("DELETE FROM test_parents")
	db.Exec("DELETE FROM test_children")
}

// TestDoCreate_CascadeMultipleCascadeRelations 多个级联关系（模拟多级联动）。
func TestDoCreate_CascadeMultipleCascadeRelations(t *testing.T) {
	db := getTestDB()

	// 配置两个级联关系（都指向同一个 child handler，但不同 childrenField）
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "primary_children", FKField: "parent_id", OnCreate: true},
		{HandlerName: "child", ChildrenField: "secondary_children", FKField: "parent_id", OnCreate: true},
	}

	handlerReg := NewHandlerRegistry()
	handlerReg.Register("child", newChildHandler())
	tc := NewTxCoordinator(db)
	h := newTestHandler(cascades, handlerReg, tc)

	rawMaps := []map[string]any{
		{
			"name": "parent1",
			"primary_children": []map[string]any{
				{"name": "pc1"},
			},
			"secondary_children": []map[string]any{
				{"name": "sc1"},
				{"name": "sc2"},
			},
		},
	}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, rawMaps)

	reqs := []service.CrudRequest[testParent]{
		makeReq(rawMaps[0]),
	}

	results, err := h._doCreate(ctx, reqs)
	if err != nil {
		t.Fatalf("_doCreate 失败: %v", err)
	}
	if len(results) != 1 {
		t.Fatal("父记录创建失败")
	}

	var childCount int64
	db.Model(&testChild{}).Count(&childCount)
	if childCount != 3 {
		t.Fatalf("期望 3 条子记录 (1+2), 实际 %d", childCount)
	}

	db.Exec("DELETE FROM test_children")
	db.Exec("DELETE FROM test_parents")
}

// TestDoCreate_CascadeEmptyChildrenArray children 字段存在但是空数组。
func TestDoCreate_CascadeEmptyChildrenArray(t *testing.T) {
	db := getTestDB()
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnCreate: true},
	}
	handlerReg := NewHandlerRegistry()
	handlerReg.Register("child", newChildHandler())
	tc := NewTxCoordinator(db)
	h := newTestHandler(cascades, handlerReg, tc)

	rawMaps := []map[string]any{
		{"name": "p1", "children": []map[string]any{}}, // 空数组
	}
	ctx := context.WithValue(context.Background(), rawCreateMapsKey{}, rawMaps)

	reqs := []service.CrudRequest[testParent]{
		makeReq(rawMaps[0]),
	}

	results, err := h._doCreate(ctx, reqs)
	if err != nil {
		t.Fatalf("_doCreate 失败: %v", err)
	}
	if len(results) != 1 {
		t.Fatal("父记录创建失败")
	}

	var childCount int64
	db.Model(&testChild{}).Count(&childCount)
	if childCount != 0 {
		t.Fatalf("子表应为空, 实际 %d", childCount)
	}

	db.Exec("DELETE FROM test_parents")
	db.Exec("DELETE FROM test_children")
}

// TestExtractChildData 单独测试 extractChildData 工具函数。
func TestExtractChildData(t *testing.T) {
	raw := map[string]any{
		"name": "test",
		"items": []any{
			map[string]any{"id": 1, "val": "a"},
			map[string]any{"id": 2, "val": "b"},
		},
	}

	// 正常提取
	items := extractChildData(raw, "items", "")
	if len(items) != 2 {
		t.Fatalf("期望 2 条, 实际 %d", len(items))
	}
	if items[0]["id"] != 1 || items[1]["id"] != 2 {
		t.Fatal("数据不匹配")
	}

	// 不存在字段
	missing := extractChildData(raw, "nonexistent", "")
	if len(missing) != 0 {
		t.Fatal("应返回空")
	}

	// nil 值
	rawNil := map[string]any{"items": nil}
	missing2 := extractChildData(rawNil, "items", "")
	if len(missing2) != 0 {
		t.Fatal("nil 值应返回空")
	}

	// 非数组
	rawStr := map[string]any{"items": "not_array"}
	missing3 := extractChildData(rawStr, "items", "")
	if len(missing3) != 0 {
		t.Fatal("非数组应返回空")
	}

	// ========== 标量数组 + wrapKey ==========

	// 前端传 [1, 2, 3] → 包裹为 [{"tag_id":1}, {"tag_id":2}, {"tag_id":3}]
	rawScalar := map[string]any{"tags": []any{1, 2, 3}}
	wrapped := extractChildData(rawScalar, "tags", "tag_id")
	if len(wrapped) != 3 {
		t.Fatalf("标量包裹: 期望 3 条, 实际 %d", len(wrapped))
	}
	if wrapped[0]["tag_id"] != 1 || wrapped[1]["tag_id"] != 2 || wrapped[2]["tag_id"] != 3 {
		t.Fatalf("标量包裹: 数据不匹配, 实际 %+v", wrapped)
	}

	// wrapKey 为空时，标量数组应被忽略（不包裹）
	unwrapped := extractChildData(rawScalar, "tags", "")
	if len(unwrapped) != 0 {
		t.Fatalf("wrapKey 为空时标量应被忽略, 实际 %d 条", len(unwrapped))
	}

	// 混合数组：部分 map + 部分标量
	rawMixed := map[string]any{
		"tags": []any{
			map[string]any{"tag_id": 1},
			2,
			map[string]any{"tag_id": 3},
		},
	}
	mixed := extractChildData(rawMixed, "tags", "tag_id")
	if len(mixed) != 3 {
		t.Fatalf("混合数组: 期望 3 条, 实际 %d", len(mixed))
	}
	if mixed[0]["tag_id"] != 1 {
		t.Fatal("混合数组[0] 期望 id=1")
	}
	if mixed[1]["tag_id"] != 2 {
		t.Fatal("混合数组[1] 期望 id=2（由标量包裹）")
	}
	if mixed[2]["tag_id"] != 3 {
		t.Fatal("混合数组[2] 期望 id=3")
	}
}

// TestHasCascadesOnCreate 单独测试 hasCascadesOnCreate。
func TestHasCascadesOnCreate(t *testing.T) {
	tests := []struct {
		name     string
		cascades []CascadeRelation
		want     bool
	}{
		{"nil cascades", nil, false},
		{"empty cascades", []CascadeRelation{}, false},
		{"OnCreate false", []CascadeRelation{{OnCreate: false}}, false},
		{"OnCreate true", []CascadeRelation{{OnCreate: true}}, true},
		{"mixed", []CascadeRelation{{OnCreate: false}, {OnCreate: true}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &GenericHandler[testParent]{
				config: HandlerConfig[testParent]{Cascades: tt.cascades},
			}
			if got := h.hasCascadesOnCreate(); got != tt.want {
				t.Fatalf("hasCascadesOnCreate() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ============================================================
// 辅助：为级联更新测试准备数据
// ============================================================

// seedUpdateTestData 创建一个父实体+子记录，返回父实体。
func seedUpdateTestData(db *gorm.DB) *testParent {
	p := &testParent{Name: "upd_parent"}
	db.Create(p)
	c1 := &testChild{ParentID: fmt.Sprintf("%d", p.ID), Name: "c1_old"}
	c2 := &testChild{ParentID: fmt.Sprintf("%d", p.ID), Name: "c2_old"}
	db.Create(c1)
	db.Create(c2)
	return p
}

// ============================================================
// 级联更新测试
// ============================================================

// TestDoUpdate_NoCascade 无级联配置 → 仅更新父记录。
func TestDoUpdate_NoCascade(t *testing.T) {
	db := getTestDB()
	h := newTestHandler(nil, nil, nil)

	p := seedUpdateTestData(db)
	raw := map[string]any{"id": p.ID, "name": "updated_no_cascade"}
	req := makeReq(raw)
	ctx := context.Background()

	results, err := h._doUpdate(ctx, []service.CrudRequest[testParent]{req}, false)
	if err != nil {
		t.Fatalf("_doUpdate 失败: %v", err)
	}
	if results[0].Name != "updated_no_cascade" {
		t.Fatalf("期望 Name='updated_no_cascade', 实际 '%s'", results[0].Name)
	}

	// 子记录不受影响
	var children []testChild
	db.Where("parent_id = ?", fmt.Sprintf("%d", p.ID)).Find(&children)
	if len(children) != 2 {
		t.Fatalf("无级联时子记录应保持 2 条, 实际 %d", len(children))
	}

	cleanupTestTables(db)
}

// TestDoUpdate_CascadeNonVersioned 非版本化父实体 → 级联更新子记录。
func TestDoUpdate_CascadeNonVersioned(t *testing.T) {
	db := getTestDB()
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnUpdate: true},
	}
	handlerReg := NewHandlerRegistry()
	handlerReg.Register("child", newChildHandler())
	tc := NewTxCoordinator(db)
	h := newTestHandler(cascades, handlerReg, tc)

	p := seedUpdateTestData(db)

	// 更新父 + 新的子数据（替换旧子记录）
	raw := map[string]any{
		"id":   p.ID,
		"name": "updated_parent",
		"children": []map[string]any{
			{"name": "new_c1"},
			{"name": "new_c2"},
			{"name": "new_c3"},
		},
	}
	req := makeReq(raw)
	ctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{raw})

	results, err := h._doUpdate(ctx, []service.CrudRequest[testParent]{req}, false)
	if err != nil {
		t.Fatalf("级联更新失败: %v", err)
	}
	if results[0].Name != "updated_parent" {
		t.Fatalf("期望 Name='updated_parent', 实际 '%s'", results[0].Name)
	}

	// 旧子记录应被软删除（非版本化 → 先删后建）
	var oldChildren []testChild
	db.Where("parent_id = ? AND is_deleted = 1", fmt.Sprintf("%d", p.ID)).Find(&oldChildren)
	if len(oldChildren) != 2 {
		t.Fatalf("旧子记录应被软删除 2 条, 实际软删除 %d", len(oldChildren))
	}

	// 新子记录应创建 3 条
	var newChildren []testChild
	db.Where("parent_id = ? AND is_deleted = 0", fmt.Sprintf("%d", p.ID)).Find(&newChildren)
	if len(newChildren) != 3 {
		t.Fatalf("期望 3 条新子记录, 实际 %d", len(newChildren))
	}

	cleanupTestTables(db)
}

// TestDoUpdate_CascadeOnUpdateFalse OnUpdate=false → 跳过级联。
func TestDoUpdate_CascadeOnUpdateFalse(t *testing.T) {
	db := getTestDB()
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnUpdate: false},
	}
	handlerReg := NewHandlerRegistry()
	handlerReg.Register("child", newChildHandler())
	tc := NewTxCoordinator(db)
	h := newTestHandler(cascades, handlerReg, tc)

	p := seedUpdateTestData(db)
	raw := map[string]any{"id": p.ID, "name": "updated", "children": []map[string]any{{"name": "new_c1"}}}
	req := makeReq(raw)
	ctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{raw})

	_, err := h._doUpdate(ctx, []service.CrudRequest[testParent]{req}, false)
	if err != nil {
		t.Fatalf("_doUpdate 失败: %v", err)
	}

	// OnUpdate=false → 子记录不受影响
	var children []testChild
	db.Where("parent_id = ?", fmt.Sprintf("%d", p.ID)).Find(&children)
	if len(children) != 2 {
		t.Fatalf("OnUpdate=false 时子记录应保持 2 条, 实际 %d", len(children))
	}

	cleanupTestTables(db)
}

// TestDoUpdate_CascadeMissingTxCoord 有级联但缺 TxCoordinator → fallback。
func TestDoUpdate_CascadeMissingTxCoord(t *testing.T) {
	db := getTestDB()
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnUpdate: true},
	}
	h := newTestHandler(cascades, nil, nil)

	p := seedUpdateTestData(db)
	raw := map[string]any{"id": p.ID, "name": "updated"}
	req := makeReq(raw)
	ctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{raw})

	_, err := h._doUpdate(ctx, []service.CrudRequest[testParent]{req}, false)
	if err != nil {
		t.Fatalf("缺 TxCoordinator 时 _doUpdate 失败: %v", err)
	}

	cleanupTestTables(db)
}

// TestDoUpdate_CascadeMissingHandlerReg 有级联但缺 HandlerRegistry → fallback。
func TestDoUpdate_CascadeMissingHandlerReg(t *testing.T) {
	db := getTestDB()
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnUpdate: true},
	}
	tc := NewTxCoordinator(db)
	h := newTestHandler(cascades, nil, tc)

	p := seedUpdateTestData(db)
	raw := map[string]any{"id": p.ID, "name": "updated"}
	req := makeReq(raw)
	ctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{raw})

	_, err := h._doUpdate(ctx, []service.CrudRequest[testParent]{req}, false)
	if err != nil {
		t.Fatalf("缺 HandlerRegistry 时 _doUpdate 失败: %v", err)
	}

	cleanupTestTables(db)
}

// TestDoUpdate_CascadeChildHandlerNotFound 子 Handler 未注册 → 跳过级联。
func TestDoUpdate_CascadeChildHandlerNotFound(t *testing.T) {
	db := getTestDB()
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnUpdate: true},
	}
	handlerReg := NewHandlerRegistry()
	// 不注册 child handler
	tc := NewTxCoordinator(db)
	h := newTestHandler(cascades, handlerReg, tc)

	p := seedUpdateTestData(db)
	raw := map[string]any{"id": p.ID, "name": "updated", "children": []map[string]any{{"name": "new_c1"}}}
	req := makeReq(raw)
	ctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{raw})

	_, err := h._doUpdate(ctx, []service.CrudRequest[testParent]{req}, false)
	if err != nil {
		t.Fatalf("子 handler 未注册时 _doUpdate 失败: %v", err)
	}

	// 子记录不受影响（handler 未找到 → 跳过）
	var children []testChild
	db.Where("parent_id = ?", fmt.Sprintf("%d", p.ID)).Find(&children)
	if len(children) != 2 {
		t.Fatalf("handler 未注册时子记录应保持 2 条, 实际 %d", len(children))
	}

	cleanupTestTables(db)
}

// TestDoUpdate_CascadeTransactionRollback 子更新失败 → 事务回滚，父也未更新。
func TestDoUpdate_CascadeTransactionRollback(t *testing.T) {
	db := getTestDB()
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnUpdate: true},
	}
	handlerReg := NewHandlerRegistry()
	handlerReg.Register("child", &updateErrorChildHandler{})
	tc := NewTxCoordinator(db)
	h := newTestHandler(cascades, handlerReg, tc)

	p := seedUpdateTestData(db)
	oldName := p.Name
	raw := map[string]any{
		"id":       p.ID,
		"name":     "should_rollback",
		"children": []map[string]any{{"name": "new_c1"}},
	}
	req := makeReq(raw)
	ctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{raw})

	_, err := h._doUpdate(ctx, []service.CrudRequest[testParent]{req}, false)
	if err == nil {
		t.Fatal("期望返回错误（子更新失败），但 err 为 nil")
	}

	// 事务回滚 → 父名称未变
	var parent testParent
	db.First(&parent, p.ID)
	if parent.Name != oldName {
		t.Fatalf("事务回滚后 Name 应保持 '%s', 实际 '%s'", oldName, parent.Name)
	}

	cleanupTestTables(db)
}

// TestDoUpdate_CascadeNoChildData raw 中无子数据 → 回填旧子数据后更新，结果不变。
func TestDoUpdate_CascadeNoChildData(t *testing.T) {
	db := getTestDB()
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnUpdate: true},
	}
	handlerReg := NewHandlerRegistry()
	handlerReg.Register("child", newChildHandler())
	tc := NewTxCoordinator(db)
	h := newTestHandler(cascades, handlerReg, tc)

	p := seedUpdateTestData(db)
	raw := map[string]any{"id": p.ID, "name": "updated_no_children"} // 无 children 字段
	req := makeReq(raw)
	ctx := context.WithValue(context.Background(), rawUpdateMapsKey{}, []map[string]any{raw})

	results, err := h._doUpdate(ctx, []service.CrudRequest[testParent]{req}, false)
	if err != nil {
		t.Fatalf("无子数据时 _doUpdate 失败: %v", err)
	}
	if results[0].Name != "updated_no_children" {
		t.Fatalf("期望 Name='updated_no_children', 实际 '%s'", results[0].Name)
	}

	// raw 中无 children 字段 → 回填旧子数据后调用 DoUpdate（走 updatePipeline，子记录被更新）
	// 旧子记录保持不变（非版本化+回填 → 子记录原地更新）
	var children []testChild
	db.Where("parent_id = ? AND is_deleted = 0", fmt.Sprintf("%d", p.ID)).Find(&children)
	if len(children) != 2 {
		t.Fatalf("无子数据字段时未删除子记录应保持 2 条, 实际 %d", len(children))
	}

	cleanupTestTables(db)
}

// TestExtractPKFromResult 单独测试 extractPKFromResult。
func TestExtractPKFromResult(t *testing.T) {
	// 通过 GetULID 接口
	p := &testParent{ID: 42, Name: "test"}
	pk := extractPKFromResult(p)
	if pk != "42" {
		t.Fatalf("期望 '42', 实际 %v", pk)
	}
}

// ============================================================
// 辅助：为级联删除测试准备数据（创建父 + 子记录）
// 返回 (父ID列表, 子ID列表)
// ============================================================

func seedDeleteTestData(db *gorm.DB) (parentIDs []any, childIDs []uint) {
	// 创建 2 个父实体，各带子记录
	p1 := &testParent{Name: "del_p1"}
	p2 := &testParent{Name: "del_p2"}
	db.Create(p1)
	db.Create(p2)
	parentIDs = []any{p1.ID, p2.ID}

	c1 := &testChild{ParentID: fmt.Sprintf("%d", p1.ID), Name: "c1_1"}
	c2 := &testChild{ParentID: fmt.Sprintf("%d", p1.ID), Name: "c1_2"}
	c3 := &testChild{ParentID: fmt.Sprintf("%d", p2.ID), Name: "c2_1"}
	db.Create(c1)
	db.Create(c2)
	db.Create(c3)
	childIDs = []uint{c1.ID, c2.ID, c3.ID}
	return
}

// ============================================================
// 级联删除测试
// ============================================================

// TestDoDelete_NoCascade 无级联配置 → 仅删父记录。
func TestDoDelete_NoCascade(t *testing.T) {
	db := getTestDB()
	h := newTestHandler(nil, nil, nil)

	parentIDs, _ := seedDeleteTestData(db)
	// 只删 p1
	err := h._doDelete(context.Background(), []any{parentIDs[0]}, nil)
	if err != nil {
		t.Fatalf("_doDelete 失败: %v", err)
	}

	// p1 应被软删除（is_deleted=1），p2 不受影响
	var p1, p2 testParent
	db.First(&p1, parentIDs[0])
	db.First(&p2, parentIDs[1])
	if p1.IsDeleted != 1 {
		t.Fatal("p1 应被软删除")
	}
	if p2.IsDeleted != 0 {
		t.Fatal("p2 不应被删除")
	}

	cleanupTestTables(db)
}

// TestDoDelete_CascadeSuccess 级联删除：父 + 子同时软删除。
func TestDoDelete_CascadeSuccess(t *testing.T) {
	db := getTestDB()
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnDelete: true},
	}
	handlerReg := NewHandlerRegistry()
	handlerReg.Register("child", newChildHandler())
	tc := NewTxCoordinator(db)
	h := newTestHandler(cascades, handlerReg, tc)

	parentIDs, _ := seedDeleteTestData(db)

	// 删 p1 → p1 和 c1_1、c1_2 都应被软删除，p2 和 c2_1 不受影响
	err := h._doDelete(context.Background(), []any{parentIDs[0]}, nil)
	if err != nil {
		t.Fatalf("级联删除失败: %v", err)
	}

	// 验证 p1 被软删除
	var p1 testParent
	db.First(&p1, parentIDs[0])
	if p1.IsDeleted != 1 {
		t.Fatal("p1 应被删除")
	}

	// 验证 p2 未受影响
	var p2 testParent
	db.First(&p2, parentIDs[1])
	if p2.IsDeleted != 0 {
		t.Fatal("p2 不应被删除")
	}

	// 验证子记录：c1_1、c1_2 软删除，c2_1 未受影响
	var allChildren []testChild
	db.Find(&allChildren)
	for _, c := range allChildren {
		if c.ParentID == fmt.Sprintf("%d", parentIDs[0]) && c.IsDeleted != 1 {
			t.Fatalf("子记录 %d (parent=%s) 应被软删除", c.ID, c.ParentID)
		}
		if c.ParentID == fmt.Sprintf("%d", parentIDs[1]) && c.IsDeleted != 0 {
			t.Fatalf("子记录 %d (parent=%s) 不应被删除", c.ID, c.ParentID)
		}
	}

	cleanupTestTables(db)
}

// TestDoDelete_CascadeOnDeleteFalse OnDelete=false → 跳过级联，仅删父。
func TestDoDelete_CascadeOnDeleteFalse(t *testing.T) {
	db := getTestDB()
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnDelete: false},
	}
	handlerReg := NewHandlerRegistry()
	handlerReg.Register("child", newChildHandler())
	tc := NewTxCoordinator(db)
	h := newTestHandler(cascades, handlerReg, tc)

	parentIDs, _ := seedDeleteTestData(db)

	err := h._doDelete(context.Background(), []any{parentIDs[0]}, nil)
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	// 父被删，子未受影响（因为 OnDelete=false）
	var allChildren []testChild
	db.Find(&allChildren)
	for _, c := range allChildren {
		if c.IsDeleted != 0 {
			t.Fatalf("子记录 %d 不应被删除（OnDelete=false）", c.ID)
		}
	}

	cleanupTestTables(db)
}

// TestDoDelete_CascadeMissingTxCoord 有级联但缺 TxCoordinator → fallback。
func TestDoDelete_CascadeMissingTxCoord(t *testing.T) {
	db := getTestDB()
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnDelete: true},
	}
	h := newTestHandler(cascades, nil, nil)

	parentIDs, _ := seedDeleteTestData(db)
	err := h._doDelete(context.Background(), []any{parentIDs[0]}, nil)
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	cleanupTestTables(db)
}

// TestDoDelete_CascadeMissingHandlerReg 有级联但缺 HandlerRegistry → fallback。
func TestDoDelete_CascadeMissingHandlerReg(t *testing.T) {
	db := getTestDB()
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnDelete: true},
	}
	tc := NewTxCoordinator(db)
	h := newTestHandler(cascades, nil, tc)

	parentIDs, _ := seedDeleteTestData(db)
	err := h._doDelete(context.Background(), []any{parentIDs[0]}, nil)
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	cleanupTestTables(db)
}

// TestDoDelete_CascadeChildHandlerNotFound 子 Handler 未注册 → 跳过级联（仅删父）。
func TestDoDelete_CascadeChildHandlerNotFound(t *testing.T) {
	db := getTestDB()
	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnDelete: true},
	}
	handlerReg := NewHandlerRegistry()
	// 不注册 child handler
	tc := NewTxCoordinator(db)
	h := newTestHandler(cascades, handlerReg, tc)

	parentIDs, _ := seedDeleteTestData(db)
	err := h._doDelete(context.Background(), []any{parentIDs[0]}, nil)
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	// 父已删，子未受影响（handler 未找到 → 跳过）
	var allChildren []testChild
	db.Find(&allChildren)
	for _, c := range allChildren {
		if c.IsDeleted != 0 {
			t.Fatalf("子记录 %d 不应被删除（handler 未注册）", c.ID)
		}
	}

	cleanupTestTables(db)
}

// TestDoDelete_CascadeTransactionRollback 子删除失败 → 事务回滚，父也未删除。
func TestDoDelete_CascadeTransactionRollback(t *testing.T) {
	db := getTestDB()

	cascades := []CascadeRelation{
		{HandlerName: "child", ChildrenField: "children", FKField: "parent_id", OnDelete: true},
	}
	handlerReg := NewHandlerRegistry()
	handlerReg.Register("child", &errorChildHandler{})
	tc := NewTxCoordinator(db)
	h := newTestHandler(cascades, handlerReg, tc)

	parentIDs, _ := seedDeleteTestData(db)

	err := h._doDelete(context.Background(), []any{parentIDs[0]}, nil)
	if err == nil {
		t.Fatal("期望返回错误（子删除失败），但 err 为 nil")
	}

	// 事务回滚 → p1 应未被删除
	var p1 testParent
	db.First(&p1, parentIDs[0])
	if p1.IsDeleted != 0 {
		t.Fatal("事务回滚后 p1 不应被删除")
	}

	cleanupTestTables(db)
}

// TestHasCascadesOnDelete 单独测试 hasCascadesOnDelete。
func TestHasCascadesOnDelete(t *testing.T) {
	tests := []struct {
		name     string
		cascades []CascadeRelation
		want     bool
	}{
		{"nil cascades", nil, false},
		{"empty cascades", []CascadeRelation{}, false},
		{"OnDelete false", []CascadeRelation{{OnDelete: false}}, false},
		{"OnDelete true", []CascadeRelation{{OnDelete: true}}, true},
		{"mixed", []CascadeRelation{{OnDelete: false}, {OnDelete: true}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &GenericHandler[testParent]{
				config: HandlerConfig[testParent]{Cascades: tt.cascades},
			}
			if got := h.hasCascadesOnDelete(); got != tt.want {
				t.Fatalf("hasCascadesOnDelete() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestHasCascadesOnUpdate 单独测试 hasCascadesOnUpdate。
func TestHasCascadesOnUpdate(t *testing.T) {
	tests := []struct {
		name     string
		cascades []CascadeRelation
		want     bool
	}{
		{"nil cascades", nil, false},
		{"empty cascades", []CascadeRelation{}, false},
		{"OnUpdate false", []CascadeRelation{{OnUpdate: false}}, false},
		{"OnUpdate true", []CascadeRelation{{OnUpdate: true}}, true},
		{"mixed", []CascadeRelation{{OnUpdate: false}, {OnUpdate: true}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &GenericHandler[testParent]{
				config: HandlerConfig[testParent]{Cascades: tt.cascades},
			}
			if got := h.hasCascadesOnUpdate(); got != tt.want {
				t.Fatalf("hasCascadesOnUpdate() = %v, want %v", got, tt.want)
			}
		})
	}
}
