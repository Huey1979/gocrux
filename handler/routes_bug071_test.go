package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/Huey1979/gocrux/repository"
	"github.com/Huey1979/gocrux/service"
)

// ============================================================
// BUG-071 回归测试：路由级禁用（DisabledRoutes）
//
// 语义：**不注册**指定 HTTP 路由（对外表现为「路由不存在」），
// 而不是禁用 handler、也不是禁用 handler 方法 —— Create/Delete 等方法、
// DoCreate/DoList 等内部入口、级联与钩子后处理完全不受影响。
//
// 与「用 BeforeCreate 钩子返回 403」的临时方案的区别：
// 404（入口不存在）vs 403（入口存在但不允许），后者会暴露端点存在性并误导调用方。
// ============================================================

// bug071VerDoc 版本化测试实体（用于验证版本路由的禁用与注册集合）。
type bug071VerDoc struct {
	ULID          string `gorm:"column:ulid;primaryKey;size:26" json:"ulid"`
	Code          string `gorm:"column:code;size:64" json:"code"`
	Name          string `gorm:"column:name;size:100" json:"name"`
	VersionCode   string `gorm:"column:version_code;size:20" json:"version_code"`
	VersionStatus string `gorm:"column:version_status;size:20" json:"version_status"`
	IsCurrent     int8   `gorm:"column:is_current;default:0" json:"is_current"`
	ParentULID    string `gorm:"column:parent_ulid;size:26" json:"parent_ulid"`
	CreatedBy     string `gorm:"column:created_by;size:26" json:"created_by"`
	IsDeleted     int8   `gorm:"column:is_deleted;default:0" json:"-"`
}

func (d *bug071VerDoc) SetDefaults()             {}
func (d *bug071VerDoc) SetCreatedAt(_ time.Time) {}
func (d *bug071VerDoc) SetCreatedBy(uid string)  { d.CreatedBy = uid }
func (d *bug071VerDoc) SetUpdatedAt(_ time.Time) {}
func (d *bug071VerDoc) SetUpdatedBy(string)      {}
func (d *bug071VerDoc) SupportsDraft() bool      { return false }
func (d *bug071VerDoc) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *bug071VerDoc) PKField() string          { return "ulid" }
func (d *bug071VerDoc) SelfFKField() string      { return "" }

// openBug071DB 打开独立 sqlite 内存库并迁移测试表。
func openBug071DB(t *testing.T, objs ...any) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sqlDB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(objs...); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

// newBug071Handler 构造非版本化（支持软删）实体的 Handler。
func newBug071Handler(t *testing.T, disabled []string) (*GenericHandler[*bug070Doc], *gin.Engine) {
	t.Helper()
	db := openBug071DB(t, &bug070Doc{})
	svc := service.NewGenericService[*bug070Doc](repository.NewCRUDWithDB[*bug070Doc](db), service.Config[*bug070Doc]{})
	h := NewGenericHandlerWithSvc[*bug070Doc](svc, "bug071", HandlerConfig[*bug070Doc]{
		PathPrefix:     "/api/v1/bug071",
		DisabledRoutes: disabled,
	})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h.RegisterRoutes(r)
	return h, r
}

// routeSet 收集 engine 已注册的路由（"METHOD PATH" 集合）。
func routeSet(r *gin.Engine) map[string]bool {
	out := make(map[string]bool)
	for _, ri := range r.Routes() {
		out[ri.Method+" "+ri.Path] = true
	}
	return out
}

// TestBug071DisabledRouteNotRegistered 核心：被禁路由**不注册** → 请求返回 404
// （路由不存在），其余路由行为不变。
func TestBug071DisabledRouteNotRegistered(t *testing.T) {
	_, r := newBug071Handler(t, []string{"POST /create", "POST /delete"})

	// 被禁路由 → 404（不是 403）
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/bug071/create"},
		{http.MethodPost, "/api/v1/bug071/delete"},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(c.method, c.path, nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("BUG-071: %s %s = %d, want 404 (route must not exist, not 403)", c.method, c.path, w.Code)
		}
	}

	// 未禁用路由 → 正常可达（list 返回 200）
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/bug071/list", nil))
	if w.Code != http.StatusOK {
		t.Errorf("GET /list must still be registered, got %d", w.Code)
	}
	// restore 未被禁用 → 仍注册（非 404）
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/bug071/restore", nil))
	if w.Code == http.StatusNotFound {
		t.Error("POST /restore must still be registered (not in DisabledRoutes)")
	}
}

// TestBug071DisabledRouteKeepsHandlerAbility 禁路由不禁能力：
// handler 的 DoCreate 仍可调用并成功落库（内部/级联写入不受影响）。
func TestBug071DisabledRouteKeepsHandlerAbility(t *testing.T) {
	h, _ := newBug071Handler(t, []string{"POST /create", "POST /delete"})

	ids, err := h.DoCreate(context.Background(), []map[string]any{{"name": "internal-write"}})
	if err != nil {
		t.Fatalf("BUG-071: DoCreate must remain callable after disabling the route: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("DoCreate must create the record, got %v", ids)
	}
}

// TestBug071FullPathFormAccepted 全路径写法（"POST /api/v1/bug071/create"）同样生效。
func TestBug071FullPathFormAccepted(t *testing.T) {
	_, r := newBug071Handler(t, []string{"POST /api/v1/bug071/create"})
	set := routeSet(r)
	if set["POST /api/v1/bug071/create"] {
		t.Error("BUG-071: full-path form must disable the route too")
	}
	if !set["POST /api/v1/bug071/delete"] {
		t.Error("other routes must remain registered")
	}
}

// TestBug071EmptyConfigRegistersAll 不回归：DisabledRoutes 为空时注册集合与修复前一致
// （非版本化软删实体：6 条基础 + restore）。
func TestBug071EmptyConfigRegistersAll(t *testing.T) {
	_, r := newBug071Handler(t, nil)
	got := routeSet(r)
	want := []string{
		"POST /api/v1/bug071/create",
		"GET /api/v1/bug071/list",
		"GET /api/v1/bug071/get",
		"POST /api/v1/bug071/update",
		"POST /api/v1/bug071/batch-update",
		"POST /api/v1/bug071/delete",
		"POST /api/v1/bug071/restore", // 支持软删 + 非版本化
	}
	for _, k := range want {
		if !got[k] {
			t.Errorf("route %s must be registered when DisabledRoutes is empty", k)
		}
	}
	if len(got) != len(want) {
		t.Errorf("route count = %d, want %d: %v", len(got), len(want), got)
	}
}

// TestBug071VersionedRoutesCanBeDisabled 版本化实体：4 条版本路由同样可禁用，
// 且未配置时全部注册（不回归）。
func TestBug071VersionedRoutesCanBeDisabled(t *testing.T) {
	db := openBug071DB(t, &bug071VerDoc{})
	svc := service.NewGenericService[*bug071VerDoc](repository.NewCRUDWithDB[*bug071VerDoc](db), service.Config[*bug071VerDoc]{
		VersionMode: true,
		VersionFields: &service.VersionFieldMapping{
			ULIDField: "ULID", CodeField: "Code", VersionField: "VersionCode",
			CurrentField: "IsCurrent", StatusField: "VersionStatus", ParentField: "ParentULID",
		},
	})
	gin.SetMode(gin.TestMode)

	// 1) 未配置：版本化路由全注册，且不注册 /restore（版本化不适用）
	all := gin.New()
	NewGenericHandlerWithSvc[*bug071VerDoc](svc, "bug071v", HandlerConfig[*bug071VerDoc]{
		PathPrefix: "/api/v1/bug071v",
	}).RegisterRoutes(all)
	set := routeSet(all)
	for _, k := range []string{
		"POST /api/v1/bug071v/activate",
		"GET /api/v1/bug071v/versions",
		"POST /api/v1/bug071v/edit-version",
		"GET /api/v1/bug071v/versions-archived",
	} {
		if !set[k] {
			t.Errorf("versioned route %s must be registered", k)
		}
	}
	if set["POST /api/v1/bug071v/restore"] {
		t.Error("/restore must NOT be registered for versioned entities")
	}

	// 2) 禁用版本路由
	dis := gin.New()
	NewGenericHandlerWithSvc[*bug071VerDoc](svc, "bug071v", HandlerConfig[*bug071VerDoc]{
		PathPrefix:     "/api/v1/bug071v",
		DisabledRoutes: []string{"POST /activate", "GET /versions"},
	}).RegisterRoutes(dis)
	set2 := routeSet(dis)
	if set2["POST /api/v1/bug071v/activate"] || set2["GET /api/v1/bug071v/versions"] {
		t.Error("BUG-071: disabled versioned routes must not be registered")
	}
	if !set2["POST /api/v1/bug071v/edit-version"] || !set2["GET /api/v1/bug071v/list"] {
		t.Error("other routes must remain registered")
	}
}

// TestBug071InvalidConfigFailsFast 配置写错必须在构造期 panic（fail-fast），
// 不能静默忽略 ——「以为禁了其实没禁」是安全问题里最糟的一类。
func TestBug071InvalidConfigFailsFast(t *testing.T) {
	db := openBug071DB(t, &bug070Doc{})
	svc := service.NewGenericService[*bug070Doc](repository.NewCRUDWithDB[*bug070Doc](db), service.Config[*bug070Doc]{})

	cases := []struct {
		name string
		cfg  []string
	}{
		{"未知 method", []string{"POSTT /create"}},
		{"未知路径", []string{"POST /nope"}},
		{"格式缺 method", []string{"/create"}},
		{"PUT 不被支持", []string{"PUT /update"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("BUG-071: invalid DisabledRoutes %v must panic at construction", c.cfg)
				}
			}()
			NewGenericHandlerWithSvc[*bug070Doc](svc, "bug071", HandlerConfig[*bug070Doc]{
				PathPrefix:     "/api/v1/bug071",
				DisabledRoutes: c.cfg,
			})
		})
	}
}
