//go:build dbtest
// +build dbtest

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Huey1979/gocrux/constants"
	"github.com/Huey1979/gocrux/internal/logger"
	"github.com/Huey1979/gocrux/repository"
	"github.com/Huey1979/gocrux/service"

	"github.com/gin-gonic/gin"
)

// ============================================================
// HTTP 全链路测试实体
// ============================================================

// testHttpEntity HTTP 测试用简单实体。
type testHttpEntity struct {
	ID        uint   `gorm:"primaryKey;autoIncrement" json:"id"`
	Name      string `gorm:"size:100" json:"name"`
	Status    string `gorm:"size:50;default:'active'" json:"status"`
	IsDeleted int8   `gorm:"column:is_deleted;default:0" json:"-"`
}

func (t testHttpEntity) SetDefaults()               {}
func (t testHttpEntity) SetCreatedAt(tm time.Time)  {}
func (t testHttpEntity) SetCreatedBy(userID string) {}
func (t testHttpEntity) SetUpdatedAt(tm time.Time)  {}
func (t testHttpEntity) SetUpdatedBy(userID string) {}
func (t testHttpEntity) SupportsDraft() bool        { return false }
func (t testHttpEntity) SetDelete() bool            { t.IsDeleted = 1; return true }
func (t testHttpEntity) PKField() string            { return "id" }
func (t testHttpEntity) SelfFKField() string        { return "" }

// ============================================================
// 测试环境搭建
// ============================================================

// setupHTTPTest 搭建全链路 HTTP 测试环境：Gin 引擎 + 中间件 + 路由 + 数据库清理。
// 返回 gin.Engine 和 cleanup 函数。
func setupHTTPTest(t *testing.T) (*gin.Engine, func()) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := getTestDB()

	// 创建测试表
	if err := db.AutoMigrate(&testHttpEntity{}); err != nil {
		t.Fatalf("迁移 testHttpEntity 表失败: %v", err)
	}

	cleanup := func() {
		db.Migrator().DropTable(&testHttpEntity{})
	}

	// 跳过日志中间件，直接从 context 取 user_ulid 避免日志文件干扰
	repo := repository.NewCRUDWithDB[testHttpEntity](db)
	svc := service.NewGenericService[testHttpEntity](repo, service.Config[testHttpEntity]{})

	h := &GenericHandler[testHttpEntity]{
		svc:     svc,
		svcName: "test_http",
		config: HandlerConfig[testHttpEntity]{
			PathPrefix: "/api/v1/testhttp",
		},
	}

	r := gin.New()
	// 内联中间件：注入 log_id（避开 middleware 包的循环依赖）
	r.Use(func(c *gin.Context) {
		requestID := logger.GenerateRequestID()
		c.Set(logger.RequestIDKey, requestID)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), logger.RequestIDKey, requestID))
		c.Next()
	})
	h.RegisterRoutes(r)

	return r, cleanup
}

// ============================================================
// JSON 解析辅助函数
// ============================================================

// httpResp 统一响应结构（与 handler.Response 一致）。
type httpResp struct {
	Code      int             `json:"code"`
	Msg       string          `json:"msg"`
	RequestID string          `json:"request_id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// parseResp 解析 HTTP 响应为 httpResp。
func parseResp(t *testing.T, w *httptest.ResponseRecorder) httpResp {
	t.Helper()
	var r httpResp
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatalf("解析响应 JSON 失败: %v\nbody: %s", err, w.Body.String())
	}
	return r
}

// assertCode 断言业务状态码和 HTTP 状态码。
func assertCode(t *testing.T, w *httptest.ResponseRecorder, wantHTTP int, wantBiz constants.BusinessCode) httpResp {
	t.Helper()
	if w.Code != wantHTTP {
		t.Errorf("HTTP 状态码 = %d, want %d", w.Code, wantHTTP)
	}
	r := parseResp(t, w)
	if constants.BusinessCode(r.Code) != wantBiz {
		t.Errorf("业务码 = %d, want %d (%s)\nbody: %s", r.Code, wantBiz, wantBiz.GetMsg(), w.Body.String())
	}
	return r
}

// assertCode200 断言成功（业务码 200）。
func assertCode200(t *testing.T, w *httptest.ResponseRecorder) httpResp {
	t.Helper()
	return assertCode(t, w, 200, constants.CodeSuccess)
}

// dataField 从响应的 data 字段中提取指定 key 的值。
func dataField(t *testing.T, r httpResp, key string) json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(r.Data, &m); err != nil {
		t.Fatalf("解析 data 字段失败: %v", err)
	}
	v, ok := m[key]
	if !ok {
		t.Fatalf("data 中缺少字段 %q", key)
	}
	return v
}

// ============================================================
// HTTP 状态码原则（重要）
// ============================================================
// 所有业务场景（成功/失败/参数错误/数据不存在）统一返回 HTTP 200。
// 业务结果通过响应体中的 code 字段（constants.BusinessCode）和 msg 字段区分：
//   - CodeSuccess (200)：操作成功
//   - CodeNotFound (404)：数据不存在（路由存在！不要返回 HTTP 404）
//   - CodeParamError (4002)：参数校验失败
//   - CodeInternalError (500)：服务器内部异常
//
// HTTP 状态码仅用于以下场景：
//   - 路由不存在（如 /got 拼写错误）→ gin 返回 HTTP 404
//   - 服务器崩溃/panic → 中间件返回 HTTP 500
//
// ⚠️ 绝不能用业务 code 接管 HTTP 状态码，反之亦然。
// ============================================================

// ============================================================
// 测试用例
// ============================================================

// TestHTTPCreate 测试 POST /{prefix}/create
func TestHTTPCreate(t *testing.T) {
	r, cleanup := setupHTTPTest(t)
	defer cleanup()

	// 创建一条记录
	body := `[{"name": "测试项目", "status": "active"}]`
	req := httptest.NewRequest("POST", "/api/v1/testhttp/create", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := assertCode200(t, w)
	items := dataField(t, resp, "items")

	var itemsArr []map[string]any
	if err := json.Unmarshal(items, &itemsArr); err != nil {
		t.Fatalf("解析 items 失败: %v", err)
	}
	if len(itemsArr) != 1 {
		t.Fatalf("items 长度 = %d, want 1", len(itemsArr))
	}
	if itemsArr[0]["name"] != "测试项目" {
		t.Errorf("name = %v, want 测试项目", itemsArr[0]["name"])
	}
	if itemsArr[0]["id"] == nil || itemsArr[0]["id"].(float64) == 0 {
		t.Error("id 不应为 nil 或 0")
	}
}

// TestHTTPCreateBatch 测试批量创建
func TestHTTPCreateBatch(t *testing.T) {
	r, cleanup := setupHTTPTest(t)
	defer cleanup()

	body := `[{"name": "项目A"}, {"name": "项目B"}, {"name": "项目C"}]`
	req := httptest.NewRequest("POST", "/api/v1/testhttp/create", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := assertCode200(t, w)
	items := dataField(t, resp, "items")

	var itemsArr []map[string]any
	json.Unmarshal(items, &itemsArr)
	if len(itemsArr) != 3 {
		t.Errorf("items 长度 = %d, want 3", len(itemsArr))
	}
}

// TestHTTPCreateEmpty 测试空数组创建
func TestHTTPCreateEmpty(t *testing.T) {
	r, cleanup := setupHTTPTest(t)
	defer cleanup()

	body := `[]`
	req := httptest.NewRequest("POST", "/api/v1/testhttp/create", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assertCode(t, w, 200, constants.CodeParamError)
}

// TestHTTPCreateInvalidJSON 测试无效 JSON
// 原则：无效请求体是业务层面的问题，HTTP 状态码仍为 200，
// 通过响应体中的 code/msg 字段告知调用方参数错误。
func TestHTTPCreateInvalidJSON(t *testing.T) {
	r, cleanup := setupHTTPTest(t)
	defer cleanup()

	body := `not a json`
	req := httptest.NewRequest("POST", "/api/v1/testhttp/create", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// HTTP 状态码必须是 200（不能因为业务错误修改 HTTP 状态码）
	if w.Code != 200 {
		t.Errorf("HTTP 状态码 = %d, want 200（业务错误不应影响 HTTP 状态码）", w.Code)
	}
	// 响应体必须包含错误信息
	if w.Body.Len() == 0 {
		t.Error("无效 JSON 应当返回包含错误信息的响应体")
	}
}

// TestHTTPGet 测试 GET /{prefix}/get?id=xxx
func TestHTTPGet(t *testing.T) {
	r, cleanup := setupHTTPTest(t)
	defer cleanup()

	// 先创建一条记录
	createBody := `[{"name": "查询测试", "status": "active"}]`
	req := httptest.NewRequest("POST", "/api/v1/testhttp/create", strings.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	resp := assertCode200(t, w)
	items := dataField(t, resp, "items")
	var itemsArr []map[string]any
	json.Unmarshal(items, &itemsArr)
	id := itemsArr[0]["id"]

	// 按 id 查询
	req2 := httptest.NewRequest("GET", "/api/v1/testhttp/get?id="+formatID(id), nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	resp2 := assertCode200(t, w2)

	var data map[string]any
	if err := json.Unmarshal(resp2.Data, &data); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	getData, ok := data["data"].(map[string]any)
	if !ok {
		t.Fatalf("data.data 不是 map: %T", data["data"])
	}
	if getData["name"] != "查询测试" {
		t.Errorf("name = %v, want 查询测试", getData["name"])
	}
}

// TestHTTPGetNotFound 测试查询不存在的记录
func TestHTTPGetNotFound(t *testing.T) {
	r, cleanup := setupHTTPTest(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/v1/testhttp/get?id=99999", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assertCode(t, w, 200, constants.CodeNotFound)
}

// TestHTTPGetMissingParam 测试缺少 id 参数
func TestHTTPGetMissingParam(t *testing.T) {
	r, cleanup := setupHTTPTest(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/v1/testhttp/get", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assertCode(t, w, 200, constants.CodeParamError)
}

// TestHTTPList 测试 GET /{prefix}/list
func TestHTTPList(t *testing.T) {
	r, cleanup := setupHTTPTest(t)
	defer cleanup()

	// 批量创建
	body := `[{"name":"列表A","status":"active"},{"name":"列表B","status":"inactive"},{"name":"列表C","status":"active"}]`
	req := httptest.NewRequest("POST", "/api/v1/testhttp/create", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assertCode200(t, w)

	// 列表查询（带分页）
	req2 := httptest.NewRequest("GET", "/api/v1/testhttp/list?page=1&page_size=2", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	resp2 := assertCode200(t, w2)

	var data map[string]any
	json.Unmarshal(resp2.Data, &data)

	// 检查 items
	items, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("items 不是数组: %T", data["items"])
	}
	if len(items) != 2 {
		t.Errorf("items 长度 = %d, want 2 (page_size=2)", len(items))
	}

	// 检查 total（至少 3 条）
	total, ok := data["total"].(float64)
	if !ok {
		t.Fatalf("total 不是数字: %T", data["total"])
	}
	if total < 3 {
		t.Errorf("total = %.0f, want >= 3", total)
	}
}

// TestHTTPListWithFilter 测试列表查询带过滤条件
func TestHTTPListWithFilter(t *testing.T) {
	r, cleanup := setupHTTPTest(t)
	defer cleanup()

	// 批量创建
	body := `[{"name":"过滤A","status":"active"},{"name":"过滤B","status":"inactive"}]`
	req := httptest.NewRequest("POST", "/api/v1/testhttp/create", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assertCode200(t, w)

	// 按 status=active 过滤
	req2 := httptest.NewRequest("GET", "/api/v1/testhttp/list?status=active", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	resp2 := assertCode200(t, w2)

	var data map[string]any
	json.Unmarshal(resp2.Data, &data)
	items := data["items"].([]any)
	for _, item := range items {
		m := item.(map[string]any)
		if m["status"] != "active" {
			t.Errorf("过滤返回了非 active 记录: %v", m)
		}
	}
}

// TestHTTPUpdate 测试 POST /{prefix}/update
func TestHTTPUpdate(t *testing.T) {
	r, cleanup := setupHTTPTest(t)
	defer cleanup()

	// 先创建
	createBody := `[{"name": "老名称", "status": "active"}]`
	req := httptest.NewRequest("POST", "/api/v1/testhttp/create", strings.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	resp := assertCode200(t, w)
	items := dataField(t, resp, "items")
	var itemsArr []map[string]any
	json.Unmarshal(items, &itemsArr)
	id := itemsArr[0]["id"]

	// 更新
	updateBody := `{"id": ` + formatID(id) + `, "name": "新名称", "status": "inactive"}`
	req2 := httptest.NewRequest("POST", "/api/v1/testhttp/update", strings.NewReader(updateBody))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	resp2 := assertCode200(t, w2)

	var data map[string]any
	json.Unmarshal(resp2.Data, &data)
	result, ok := data["data"].(map[string]any)
	if !ok {
		result = data // fallback
	}
	if result["name"] != "新名称" {
		t.Errorf("name = %v, want 新名称", result["name"])
	}
	if result["status"] != "inactive" {
		t.Errorf("status = %v, want inactive", result["status"])
	}

	// 再查一次确认
	req3 := httptest.NewRequest("GET", "/api/v1/testhttp/get?id="+formatID(id), nil)
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	resp3 := assertCode200(t, w3)
	var getData map[string]any
	json.Unmarshal(resp3.Data, &getData)
	getResult := getData["data"].(map[string]any)
	if getResult["name"] != "新名称" {
		t.Errorf("查询验证: name = %v, want 新名称", getResult["name"])
	}
}

// TestHTTPUpdateMissingID 测试更新无 id
func TestHTTPUpdateMissingID(t *testing.T) {
	r, cleanup := setupHTTPTest(t)
	defer cleanup()

	body := `{"name": "无ID的更新"}`
	req := httptest.NewRequest("POST", "/api/v1/testhttp/update", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assertCode(t, w, 200, constants.CodeParamError)
}

// TestHTTPDelete 测试 POST /{prefix}/delete
// 注意：CRUDRepository 的 GetByID 不自动过滤 is_deleted=0，
// 因此软删除后记录仍可查到（标记已完成，过滤由上层负责）。
func TestHTTPDelete(t *testing.T) {
	r, cleanup := setupHTTPTest(t)
	defer cleanup()

	// 先创建
	createBody := `[{"name": "待删除", "status": "active"}]`
	req := httptest.NewRequest("POST", "/api/v1/testhttp/create", strings.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	resp := assertCode200(t, w)
	items := dataField(t, resp, "items")
	var itemsArr []map[string]any
	json.Unmarshal(items, &itemsArr)
	id := itemsArr[0]["id"]

	// 删除
	deleteBody := `{"ids": [` + formatID(id) + `]}`
	req2 := httptest.NewRequest("POST", "/api/v1/testhttp/delete", strings.NewReader(deleteBody))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	resp2 := assertCode200(t, w2)
	if resp2.Msg != "删除成功" {
		t.Errorf("msg = %s, want 删除成功", resp2.Msg)
	}

	// 软删除后记录仍可通过 Get 查到（CRUDRepository 不自动过滤 is_deleted）
	req3 := httptest.NewRequest("GET", "/api/v1/testhttp/get?id="+formatID(id), nil)
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	assertCode200(t, w3)
}

// TestHTTPDeleteMissingIDs 测试删除无 ids
func TestHTTPDeleteMissingIDs(t *testing.T) {
	r, cleanup := setupHTTPTest(t)
	defer cleanup()

	body := `{"ids": []}`
	req := httptest.NewRequest("POST", "/api/v1/testhttp/delete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assertCode(t, w, 200, constants.CodeParamError)
}

// TestHTTPFullCRUDLifecycle 全流程测试：创建 → 查单 → 更新 → 删前查 → 删除 → 删后确认
func TestHTTPFullCRUDLifecycle(t *testing.T) {
	r, cleanup := setupHTTPTest(t)
	defer cleanup()

	// 1. 创建
	createBody := `[{"name":"生命周期测试","status":"draft"}]`
	req := httptest.NewRequest("POST", "/api/v1/testhttp/create", strings.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	resp := assertCode200(t, w)
	items := dataField(t, resp, "items")
	var itemsArr []map[string]any
	json.Unmarshal(items, &itemsArr)
	id := itemsArr[0]["id"]
	idStr := formatID(id)

	// 2. Get 确认
	req2 := httptest.NewRequest("GET", "/api/v1/testhttp/get?id="+idStr, nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	resp2 := assertCode200(t, w2)
	var getData map[string]any
	json.Unmarshal(resp2.Data, &getData)
	getResult := getData["data"].(map[string]any)
	if getResult["name"] != "生命周期测试" {
		t.Errorf("Get: name = %v", getResult["name"])
	}
	if getResult["status"] != "draft" {
		t.Errorf("Get: status = %v", getResult["status"])
	}

	// 3. List 确认存在
	req3 := httptest.NewRequest("GET", "/api/v1/testhttp/list?page=1&page_size=100", nil)
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	resp3 := assertCode200(t, w3)
	var listData map[string]any
	json.Unmarshal(resp3.Data, &listData)
	total := listData["total"].(float64)
	if total < 1 {
		t.Errorf("List total = %.0f, want >= 1", total)
	}

	// 4. Update
	updateBody := `{"id":` + idStr + `,"name":"生命周期已更新","status":"active"}`
	req4 := httptest.NewRequest("POST", "/api/v1/testhttp/update", strings.NewReader(updateBody))
	req4.Header.Set("Content-Type", "application/json")
	w4 := httptest.NewRecorder()
	r.ServeHTTP(w4, req4)
	resp4 := assertCode200(t, w4)
	var updateData map[string]any
	json.Unmarshal(resp4.Data, &updateData)
	updateResult := updateData["data"].(map[string]any)
	if updateResult["name"] != "生命周期已更新" {
		t.Errorf("Update: name = %v", updateResult["name"])
	}
	if updateResult["status"] != "active" {
		t.Errorf("Update: status = %v", updateResult["status"])
	}

	// 5. Delete
	deleteBody := `{"ids": [` + idStr + `]}`
	req5 := httptest.NewRequest("POST", "/api/v1/testhttp/delete", strings.NewReader(deleteBody))
	req5.Header.Set("Content-Type", "application/json")
	w5 := httptest.NewRecorder()
	r.ServeHTTP(w5, req5)
	deleteResp := assertCode200(t, w5)
	if deleteResp.Msg != "删除成功" {
		t.Errorf("Delete: msg = %s", deleteResp.Msg)
	}

	// 6. 软删除后记录仍然存在（CRUDRepository 不自动过滤 is_deleted）
	req6 := httptest.NewRequest("GET", "/api/v1/testhttp/get?id="+idStr, nil)
	w6 := httptest.NewRecorder()
	r.ServeHTTP(w6, req6)
	assertCode200(t, w6)
}

// TestHTTPResponseFormat 测试统一响应格式
func TestHTTPResponseFormat(t *testing.T) {
	r, cleanup := setupHTTPTest(t)
	defer cleanup()

	// 创建
	body := `[{"name": "格式测试", "status": "active"}]`
	req := httptest.NewRequest("POST", "/api/v1/testhttp/create", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp httpResp
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Code != 200 {
		t.Errorf("code = %d", resp.Code)
	}
	if resp.Msg == "" {
		t.Error("msg 不应为空")
	}
	if resp.RequestID == "" {
		t.Error("request_id 不应为空（RequestLogger 中间件）")
	}
	if resp.Data == nil {
		t.Error("data 不应为 null")
	}

	// 错误场景：查询不存在的 id
	// 原则：接口正常工作（GET /get 路由存在），只是数据不存在，
	// HTTP 状态码必须是 200，业务结果通过 code/msg 告知前端。
	req2 := httptest.NewRequest("GET", "/api/v1/testhttp/get?id=99999", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != 200 {
		t.Errorf("HTTP 状态码 = %d, want 200（路由存在，业务错误通过 code 区分）", w2.Code)
	}

	var resp2 httpResp
	json.Unmarshal(w2.Body.Bytes(), &resp2)
	if resp2.Code != int(constants.CodeNotFound) {
		t.Errorf("错误场景 code = %d, want %d", resp2.Code, constants.CodeNotFound)
	}
	if resp2.RequestID == "" {
		t.Error("错误场景 request_id 不应为空")
	}
}

// ============================================================
// TestHTTPListFieldFilter — List 字段裁剪
// ============================================================

// TestHTTPListSkipFields 测试黑名单模式：List 跳过指定字段，Get 不受影响。
func TestHTTPListSkipFields(t *testing.T) {
	r, cleanup := setupHTTPWithConfig(t, HandlerConfig[testHttpEntity]{
		PathPrefix:     "/api/v1/testhttp",
		ListSkipFields: []string{"status"},
	})
	defer cleanup()

	// 创建
	body := `[{"name": "黑名单测试", "status": "secret"}]`
	req := httptest.NewRequest("POST", "/api/v1/testhttp/create", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	resp := assertCode200(t, w)

	items := dataField(t, resp, "items")
	var itemsArr []map[string]any
	json.Unmarshal(items, &itemsArr)
	id := itemsArr[0]["id"]

	// List：不应包含 status
	req2 := httptest.NewRequest("GET", "/api/v1/testhttp/list", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	resp2 := assertCode200(t, w2)

	var listData map[string]any
	json.Unmarshal(resp2.Data, &listData)
	listItems := listData["items"].([]any)
	if len(listItems) == 0 {
		t.Fatal("List 返回空结果")
	}
	first := listItems[0].(map[string]any)
	if _, ok := first["status"]; ok {
		t.Error("ListSkipFields: status 应该被移除，但仍然存在")
	}
	if first["name"] != "黑名单测试" {
		t.Errorf("name = %v, want 黑名单测试", first["name"])
	}

	// Get：应该包含 status
	req3 := httptest.NewRequest("GET", "/api/v1/testhttp/get?id="+formatID(id), nil)
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	resp3 := assertCode200(t, w3)
	var getData map[string]any
	json.Unmarshal(resp3.Data, &getData)
	getResult := getData["data"].(map[string]any)
	if getResult["status"] != "secret" {
		t.Errorf("Get: status = %v, want secret（Get 不受 ListSkipFields 影响）", getResult["status"])
	}
}

// TestHTTPListKeepFields 测试白名单模式：List 仅保留指定字段，Get 不受影响。
func TestHTTPListKeepFields(t *testing.T) {
	r, cleanup := setupHTTPWithConfig(t, HandlerConfig[testHttpEntity]{
		PathPrefix:     "/api/v1/testhttp",
		ListKeepFields: []string{"id", "name"},
	})
	defer cleanup()

	// 创建
	body := `[{"name": "白名单测试", "status": "active"}]`
	req := httptest.NewRequest("POST", "/api/v1/testhttp/create", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	resp := assertCode200(t, w)

	items := dataField(t, resp, "items")
	var itemsArr []map[string]any
	json.Unmarshal(items, &itemsArr)
	id := itemsArr[0]["id"]

	// List：仅应包含 id、name
	req2 := httptest.NewRequest("GET", "/api/v1/testhttp/list", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	resp2 := assertCode200(t, w2)

	var listData map[string]any
	json.Unmarshal(resp2.Data, &listData)
	listItems := listData["items"].([]any)
	first := listItems[0].(map[string]any)
	if first["name"] != "白名单测试" {
		t.Errorf("name = %v", first["name"])
	}
	if _, ok := first["status"]; ok {
		t.Error("ListKeepFields: status 不应存在")
	}
	if _, ok := first["name"]; !ok {
		t.Error("ListKeepFields: name 应该保留")
	}

	// Get：应包含全部字段
	req3 := httptest.NewRequest("GET", "/api/v1/testhttp/get?id="+formatID(id), nil)
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	resp3 := assertCode200(t, w3)
	var getData map[string]any
	json.Unmarshal(resp3.Data, &getData)
	getResult := getData["data"].(map[string]any)
	if getResult["status"] != "active" {
		t.Errorf("Get: status = %v, want active（Get 不受 ListKeepFields 影响）", getResult["status"])
	}
	if getResult["name"] != "白名单测试" {
		t.Errorf("Get: name = %v", getResult["name"])
	}
}

// TestHTTPListSkipPriority 测试 skip 优先于 keep。
func TestHTTPListSkipPriority(t *testing.T) {
	r, cleanup := setupHTTPWithConfig(t, HandlerConfig[testHttpEntity]{
		PathPrefix:     "/api/v1/testhttp",
		ListSkipFields: []string{"status"},
		ListKeepFields: []string{"id", "name", "status"},
	})
	defer cleanup()

	// 创建
	body := `[{"name": "优先级测试", "status": "hidden"}]`
	req := httptest.NewRequest("POST", "/api/v1/testhttp/create", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assertCode200(t, w)

	// List：skip 优先，应移除 status（即使 keep 包含它）
	req2 := httptest.NewRequest("GET", "/api/v1/testhttp/list", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	resp2 := assertCode200(t, w2)

	var listData map[string]any
	json.Unmarshal(resp2.Data, &listData)
	listItems := listData["items"].([]any)
	first := listItems[0].(map[string]any)
	if _, ok := first["status"]; ok {
		t.Error("skip 优先于 keep：status 应被跳过")
	}
	if first["name"] != "优先级测试" {
		t.Errorf("name = %v", first["name"])
	}
}

// TestHTTPListNoFilter 测试未配置过滤时全字段返回（向后兼容）。
func TestHTTPListNoFilter(t *testing.T) {
	r, cleanup := setupHTTPTest(t)
	defer cleanup()

	body := `[{"name": "全字段测试", "status": "active"}]`
	req := httptest.NewRequest("POST", "/api/v1/testhttp/create", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assertCode200(t, w)

	req2 := httptest.NewRequest("GET", "/api/v1/testhttp/list", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	resp2 := assertCode200(t, w2)

	var listData map[string]any
	json.Unmarshal(resp2.Data, &listData)
	listItems := listData["items"].([]any)
	first := listItems[0].(map[string]any)
	// 应包含所有业务字段
	if first["name"] != "全字段测试" {
		t.Errorf("name = %v", first["name"])
	}
	if first["status"] != "active" {
		t.Errorf("status = %v", first["status"])
	}
}

// ============================================================
// setupHTTPWithConfig — 带自定义 HandlerConfig 的测试环境
// ============================================================

func setupHTTPWithConfig(t *testing.T, cfg HandlerConfig[testHttpEntity]) (*gin.Engine, func()) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := getTestDB()
	if err := db.AutoMigrate(&testHttpEntity{}); err != nil {
		t.Fatalf("迁移 testHttpEntity 表失败: %v", err)
	}

	cleanup := func() {
		db.Migrator().DropTable(&testHttpEntity{})
	}

	repo := repository.NewCRUDWithDB[testHttpEntity](db)
	svc := service.NewGenericService[testHttpEntity](repo, service.Config[testHttpEntity]{})

	h := &GenericHandler[testHttpEntity]{
		svc:     svc,
		svcName: "test_http",
		config:  cfg,
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		requestID := logger.GenerateRequestID()
		c.Set(logger.RequestIDKey, requestID)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), logger.RequestIDKey, requestID))
		c.Next()
	})
	h.RegisterRoutes(r)

	return r, cleanup
}

// formatID 将 interface{} id 格式化为 JSON 数字字符串。
func formatID(id any) string {
	switch v := id.(type) {
	case float64:
		// JSON 数字反序列化后是 float64，需要去掉小数点避免 .0
		return fmt.Sprintf("%.0f", v)
	case int:
		return fmt.Sprintf("%d", v)
	case uint:
		return fmt.Sprintf("%d", v)
	case string:
		return v
	default:
		return fmt.Sprintf("%v", v)
	}
}
