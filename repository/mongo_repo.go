package repository

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/Huey1979/gocrux/common"
	"github.com/Huey1979/gocrux/internal/database/mongodb"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// MongoCRUDRepository MongoDB 泛型仓储，提供与 CRUDRepository 一致的 CRUD 接口。
//
// 用法：
//
//	repo := NewMongoCRUDRepository[entity.Product]("products")
//	product, err := repo.GetByID(ctx, "01Jxxx...")
type MongoCRUDRepository[M any] struct {
	coll     *mongo.Collection // 写库
	readColl *mongo.Collection // 读库（nil = 回退写库）
	pkField  string
}

// DefaultReadCollProvider 全局读库 Collection 获取器。
var DefaultReadCollProvider func(collectionName string) *mongo.Collection

// SetReadCollProvider 注入读库 Collection 获取器（由应用启动时调用）。
func SetReadCollProvider(fn func(string) *mongo.Collection) { DefaultReadCollProvider = fn }

// NewMongoCRUDRepository 创建 MongoDB 泛型仓储。
// 若已通过 SetReadCollProvider 注入读库获取器，自动配置读写分离。
func NewMongoCRUDRepository[M any](collectionName string) *MongoCRUDRepository[M] {
	r := &MongoCRUDRepository[M]{
		pkField: "_id",
	}
	if mongodb.Database != nil {
		r.coll = mongodb.Database.Collection(collectionName)
	}
	if DefaultReadCollProvider != nil {
		r.readColl = DefaultReadCollProvider(collectionName)
	}
	r.detectPK()
	return r
}

// BatchDeprecateVersions 版本化批量废弃：将当前版本标记为非当前（is_current=false, version_status=deprecated）。
// 供 Service._doDelete 在 VersionMode=true 时调用。
// 字段名统一下划线风格，与实体 bson tag 一致（BUG-039 修复）。
func (r *MongoCRUDRepository[M]) BatchDeprecateVersions(ctx context.Context, ids []any) error {
	_, err := r.coll.UpdateMany(ctx, bson.M{r.pkField: bson.M{"$in": ids}},
		bson.M{"$set": bson.M{"is_current": false, "version_status": "deprecated"}})
	return err
}

// BatchDeprecateVersionsByFK 版本化按外键批量废弃：级联删除子记录时使用。
func (r *MongoCRUDRepository[M]) BatchDeprecateVersionsByFK(ctx context.Context, fkField string, fkValues []any) error {
	_, err := r.coll.UpdateMany(ctx, bson.M{fkField: bson.M{"$in": fkValues}},
		bson.M{"$set": bson.M{"is_current": false, "version_status": "deprecated"}})
	return err
}

// SetColl 注入写库 Collection。
func (r *MongoCRUDRepository[M]) SetColl(coll *mongo.Collection) *MongoCRUDRepository[M] {
	r.coll = coll
	return r
}

// SetReadColl 注入读库 Collection（读写分离）。nil 时回退写库。
func (r *MongoCRUDRepository[M]) SetReadColl(coll *mongo.Collection) *MongoCRUDRepository[M] {
	r.readColl = coll
	return r
}

// ReadColl 返回读库 Collection。未配置或事务中回退写库。
func (r *MongoCRUDRepository[M]) ReadColl(ctx context.Context) *mongo.Collection {
	if sess := common.GetMongoSession(ctx); sess != nil {
		return r.collWithTx(ctx) // 事务中走写库
	}
	if r.readColl != nil {
		return r.readColl
	}
	return r.coll
}

// collWithTx 返回事务安全的 Collection。
// 若 ctx 中包含 mongo session → 使用 session 绑定集合；否则使用原始集合。
func (r *MongoCRUDRepository[M]) collWithTx(ctx context.Context) *mongo.Collection {
	if sess := common.GetMongoSession(ctx); sess != nil {
		// 使用 session 绑定的 Database 获取 Collection
		db := r.coll.Database()
		sessDB := sess.Client().Database(db.Name())
		return sessDB.Collection(r.coll.Name())
	}
	return r.coll
}

// SetPKField 显式设置主键列名。
func (r *MongoCRUDRepository[M]) SetPKField(column string) *MongoCRUDRepository[M] {
	r.pkField = column
	return r
}

// PKField 返回主键列名。
func (r *MongoCRUDRepository[M]) PKField() string { return r.pkField }

// ---------- 基础 CRUD ----------

// Insert 插入单条记录。
// explicitCols 忽略：Mongo 全量文档写入，无 GORM 零值忽略问题（BUG-045 仅 MySQL 需要）。
func (r *MongoCRUDRepository[M]) Insert(ctx context.Context, entity *M, _ ...string) error {
	data := toBsonDoc(r, entity)
	if _, err := r.coll.InsertOne(ctx, data); err != nil {
		return fmt.Errorf("MongoDB插入失败: %w", err)
	}
	return nil
}

// InsertBatch 批量插入。
// explicitCols 忽略：同 Insert（Mongo 无零值忽略问题）。
func (r *MongoCRUDRepository[M]) InsertBatch(ctx context.Context, entities []*M, _ ...string) error {
	docs := make([]any, len(entities))
	for i, e := range entities {
		docs[i] = toBsonDoc(r, e)
	}
	if _, err := r.coll.InsertMany(ctx, docs); err != nil {
		return fmt.Errorf("MongoDB批量插入失败: %w", err)
	}
	return nil
}

// GetByID 按主键查询。
func (r *MongoCRUDRepository[M]) GetByID(ctx context.Context, id any) (*M, error) {
	filter := bson.M{r.pkField: id}
	var result M
	if err := r.ReadColl(ctx).FindOne(ctx, filter).Decode(&result); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, fmt.Errorf("record not found")
		}
		return nil, fmt.Errorf("MongoDB查询失败: %w", err)
	}
	return &result, nil
}

// GetByField 按任意字段查询第一条。
func (r *MongoCRUDRepository[M]) GetByField(ctx context.Context, field string, value any) (*M, error) {
	filter := bson.M{field: value}
	var result M
	if err := r.ReadColl(ctx).FindOne(ctx, filter).Decode(&result); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, fmt.Errorf("record not found")
		}
		return nil, fmt.Errorf("MongoDB查询失败: %w", err)
	}
	return &result, nil
}

// ExistsByField 检查是否存在。
func (r *MongoCRUDRepository[M]) ExistsByField(ctx context.Context, field string, value any) (bool, error) {
	filter := bson.M{field: value}
	count, err := r.coll.CountDocuments(ctx, filter)
	if err != nil {
		return false, fmt.Errorf("MongoDB查询失败: %w", err)
	}
	return count > 0, nil
}

// Save 更新整条记录（按主键 upsert）。
func (r *MongoCRUDRepository[M]) Save(ctx context.Context, entity *M) error {
	data := toBsonDoc(r, entity)
	id := extractPKVal(entity, r.pkField)
	if id == nil {
		return fmt.Errorf("MongoDB保存失败: 主键为空")
	}
	filter := bson.M{r.pkField: id}
	update := bson.M{"$set": data}
	opts := options.Update().SetUpsert(true)
	if _, err := r.coll.UpdateOne(ctx, filter, update, opts); err != nil {
		return fmt.Errorf("MongoDB保存失败: %w", err)
	}
	return nil
}

// UpdateByID 按主键部分更新。
func (r *MongoCRUDRepository[M]) UpdateByID(ctx context.Context, id any, updates map[string]any) error {
	filter := bson.M{r.pkField: id}
	update := bson.M{"$set": updates}
	if _, err := r.coll.UpdateOne(ctx, filter, update); err != nil {
		return fmt.Errorf("MongoDB更新失败: %w", err)
	}
	return nil
}

// UpdateByIDs 按主键列表批量更新相同字段。
func (r *MongoCRUDRepository[M]) UpdateByIDs(ctx context.Context, ids []any, updates map[string]any) error {
	if len(ids) == 0 || len(updates) == 0 {
		return nil
	}
	filter := bson.M{r.pkField: bson.M{"$in": ids}}
	update := bson.M{"$set": updates}
	if _, err := r.coll.UpdateMany(ctx, filter, update); err != nil {
		return fmt.Errorf("MongoDB批量更新失败: %w", err)
	}
	return nil
}

// Delete 按主键删除。
func (r *MongoCRUDRepository[M]) Delete(ctx context.Context, id any) error {
	filter := bson.M{r.pkField: id}
	if _, err := r.coll.DeleteOne(ctx, filter); err != nil {
		return fmt.Errorf("MongoDB删除失败: %w", err)
	}
	return nil
}

// DeleteByFK 按外键批量删除。
func (r *MongoCRUDRepository[M]) DeleteByFK(ctx context.Context, fkField string, fkValues []any) error {
	filter := bson.M{fkField: bson.M{"$in": fkValues}}
	if _, err := r.coll.DeleteMany(ctx, filter); err != nil {
		return fmt.Errorf("MongoDB批量删除失败: %w", err)
	}
	return nil
}

// ---------- 列表查询 ----------

// List 分页列表查询。sortDoc 可选排序（bson.D 一对一排序键，如 bson.D{{Key: "created_at", Value: -1}}）。
func (r *MongoCRUDRepository[M]) List(ctx context.Context, filter bson.M, page, pageSize int, sortDoc ...bson.D) ([]M, int64, error) {
	return r.listOffset(ctx, filter, page, pageSize, 0, sortDoc...)
}

// listOffset 带起点偏移的列表查询。
// offset > 0 时直接作为 skip 使用（从 0 开始）；offset <= 0 时按 page 计算。
func (r *MongoCRUDRepository[M]) listOffset(ctx context.Context, filter bson.M, page, pageSize, offset int, sortDoc ...bson.D) ([]M, int64, error) {
	if filter == nil {
		filter = bson.M{}
	}
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	total, err := r.ReadColl(ctx).CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("MongoDB计数失败: %w", err)
	}
	skip := int64(offset)
	if skip <= 0 {
		skip = int64((page - 1) * pageSize)
	}
	opts := options.Find().SetSkip(skip).SetLimit(int64(pageSize))
	if len(sortDoc) > 0 && sortDoc[0] != nil {
		opts.SetSort(sortDoc[0])
	}
	cursor, err := r.ReadColl(ctx).Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, fmt.Errorf("MongoDB查询失败: %w", err)
	}
	defer cursor.Close(ctx)
	var results []M
	// 手动迭代解码，支持 []*T 指针类型
	for cursor.Next(ctx) {
		var elem M
		if reflect.TypeOf(elem).Kind() == reflect.Ptr {
			elem = reflect.New(reflect.TypeOf(elem).Elem()).Interface().(M)
		}
		if err := cursor.Decode(&elem); err != nil {
			return nil, 0, fmt.Errorf("MongoDB解码失败: %w", err)
		}
		results = append(results, elem)
	}
	return results, total, cursor.Err()
}

// ListAll 全量查询。
func (r *MongoCRUDRepository[M]) ListAll(ctx context.Context) ([]M, error) {
	cursor, err := r.ReadColl(ctx).Find(ctx, bson.M{})
	if err != nil {
		return nil, fmt.Errorf("MongoDB查询失败: %w", err)
	}
	defer cursor.Close(ctx)
	var results []M
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("MongoDB读取失败: %w", err)
	}
	return results, nil
}

// ListByField 按字段查询全量。
func (r *MongoCRUDRepository[M]) ListByField(ctx context.Context, field string, value any) ([]M, error) {
	cursor, err := r.ReadColl(ctx).Find(ctx, bson.M{field: value})
	if err != nil {
		return nil, fmt.Errorf("MongoDB查询失败: %w", err)
	}
	defer cursor.Close(ctx)
	var results []M
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("MongoDB读取失败: %w", err)
	}
	return results, nil
}

// ---------- Repo[M] 接口 — Batch / Filters / Tx ----------

// RunInTx MongoDB 事务包装。
func (r *MongoCRUDRepository[M]) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	sess, err := r.coll.Database().Client().StartSession()
	if err != nil {
		return err
	}
	defer sess.EndSession(ctx)

	_, err = sess.WithTransaction(ctx, func(sc mongo.SessionContext) (interface{}, error) {
		return nil, fn(sc)
	})
	return err
}

// ListByFilters 结构化过滤查询（将 Filter 转换为 bson），支持排序与 offset 起点。
func (r *MongoCRUDRepository[M]) ListByFilters(ctx context.Context, filters ListFilters) ([]M, int64, error) {
	f := toBsonFilter(filters)
	if filters.OrderBy != "" {
		dir := 1
		if filters.OrderDir == "desc" {
			dir = -1
		}
		sortDoc := bson.D{{Key: filters.OrderBy, Value: dir}}
		return r.listOffset(ctx, f, filters.Page, filters.PageSize, filters.Offset, sortDoc)
	}
	return r.listOffset(ctx, f, filters.Page, filters.PageSize, filters.Offset)
}

// RawList 实现 Repo[M] 接口。query 为 bson.M 过滤器。
func (r *MongoCRUDRepository[M]) RawList(ctx context.Context, dest any, query any, args ...any) error {
	filter, ok := query.(bson.M)
	if !ok {
		return fmt.Errorf("MongoCRUDRepository.RawList: query must be bson.M")
	}
	results, _, err := r.List(ctx, filter, 1, 0)
	if err != nil {
		return err
	}
	// dest 必须为 *[]M，通过反射赋值
	dv := reflect.ValueOf(dest)
	if dv.Kind() != reflect.Ptr || dv.IsNil() {
		return fmt.Errorf("RawList dest must be a non-nil pointer to slice, got %T", dest)
	}
	dv.Elem().Set(reflect.ValueOf(results))
	return nil
}

// BatchSoftDelete 批量软删除（is_deleted=1，与实体 bson tag 一致，BUG-039 修复）。
// BUG-043 修复：合并 SetDelete() 设置的非零附加字段（is_trashed/trashed_at 等），
// 否则实体自定义的垃圾桶/回收站语义字段会全部丢失。
func (r *MongoCRUDRepository[M]) BatchSoftDelete(ctx context.Context, ids []any) error {
	_, err := r.coll.UpdateMany(ctx, bson.M{r.pkField: bson.M{"$in": ids}}, bson.M{"$set": softDeleteSet(softDeleteProbe[M]())})
	return err
}

// BatchSoftDeleteByFK 按外键批量软删除（is_deleted=1，BUG-039 修复）。
// BUG-043 修复：与 BatchSoftDelete 一致，合并 SetDelete() 设置的附加字段。
func (r *MongoCRUDRepository[M]) BatchSoftDeleteByFK(ctx context.Context, fkField string, fkValues []any) error {
	_, err := r.coll.UpdateMany(ctx, bson.M{fkField: bson.M{"$in": fkValues}}, bson.M{"$set": softDeleteSet(softDeleteProbe[M]())})
	return err
}

// softDeleteSet 构造软删除 $set 文档：is_deleted=1 + SetDelete() 设置的非零附加字段。
// 若实体 SetDelete() 只设置 is_deleted（gentity 生成的默认形态），行为与修复前完全一致。
func softDeleteSet(m any) bson.M {
	set := bson.M{"is_deleted": int8(1)}
	if extra := softDeleteExtraFields(m); len(extra) > 0 {
		for k, v := range extra {
			if k == "is_deleted" {
				continue // 保持原写入类型，避免破坏已存数据的一致性
			}
			set[k] = v
		}
	}
	return set
}

// softDeleteExtraFields 对实体零值实例调用 SetDelete()，提取其设置的非零字段。
// 返回 nil 表示实体不支持软删或未设置附加字段（BUG-043 修复）。
func softDeleteExtraFields(m any) bson.M {
	sd, ok := m.(interface{ SetDelete() bool })
	if !ok || !sd.SetDelete() {
		return nil
	}
	data, err := bson.Marshal(m)
	if err != nil {
		return nil
	}
	var raw bson.M
	if err := bson.Unmarshal(data, &raw); err != nil {
		return nil
	}
	extra := bson.M{}
	for k, v := range raw {
		if isZeroBsonValue(v) {
			continue
		}
		extra[k] = v
	}
	return extra
}

// softDeleteProbe 创建 M 的非 nil 零值实例（M 为指针类型时分配底层结构），
// 用于在零值上安全调用 SetDelete() 提取附加字段。
func softDeleteProbe[M any]() M {
	var z M
	t := reflect.TypeOf(z)
	if t.Kind() == reflect.Ptr {
		return reflect.New(t.Elem()).Interface().(M)
	}
	return z
}

// isZeroBsonValue 判断 bson 反序列化值是否为零值。
// 零值字段跳过不合并，避免把实体其余未设置字段覆盖掉已有数据。
func isZeroBsonValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case bool:
		return !t
	case int:
		return t == 0
	case int8, int16, int32, int64:
		return reflect.ValueOf(v).Int() == 0
	case uint, uint8, uint16, uint32, uint64:
		return reflect.ValueOf(v).Uint() == 0
	case float32, float64:
		return reflect.ValueOf(v).Float() == 0
	case time.Time:
		return t.IsZero()
	case primitive.DateTime:
		// 注意：零值 time.Time{} 编码为负毫秒（非 0），需转回 time.Time 判断
		return t.Time().IsZero()
	case primitive.A:
		return len(t) == 0
	case primitive.M:
		return len(t) == 0
	default:
		return false
	}
}

// BatchFindByPK 批量按主键查询。
func (r *MongoCRUDRepository[M]) BatchFindByPK(ctx context.Context, ids []any) ([]M, error) {
	cursor, err := r.ReadColl(ctx).Find(ctx, bson.M{r.pkField: bson.M{"$in": ids}})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var results []M
	if err := cursor.All(ctx, &results); err != nil {
		return nil, err
	}
	return results, nil
}

// BatchFindByFK 批量按外键查询。
func (r *MongoCRUDRepository[M]) BatchFindByFK(ctx context.Context, fkField string, fkValues []any) ([]M, error) {
	cursor, err := r.ReadColl(ctx).Find(ctx, bson.M{fkField: bson.M{"$in": fkValues}})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var results []M
	if err := cursor.All(ctx, &results); err != nil {
		return nil, err
	}
	return results, nil
}

// BatchHardDelete 批量硬删除。
func (r *MongoCRUDRepository[M]) BatchHardDelete(ctx context.Context, ids []any) error {
	_, err := r.coll.DeleteMany(ctx, bson.M{r.pkField: bson.M{"$in": ids}})
	return err
}

// BatchHardDeleteByFK 按外键批量硬删除。
func (r *MongoCRUDRepository[M]) BatchHardDeleteByFK(ctx context.Context, fkField string, fkValues []any) error {
	_, err := r.coll.DeleteMany(ctx, bson.M{fkField: bson.M{"$in": fkValues}})
	return err
}

// toBsonFilter 将 ListFilters 转为 MongoDB bson 查询条件。
// 单个 Filter 直接转换，多个 Filter 用 $and 包装。
func toBsonFilter(f ListFilters) bson.M {
	if len(f.Filters) == 0 {
		return bson.M{}
	}
	if len(f.Filters) == 1 {
		return filterToBson(f.Filters[0])
	}
	and := make([]bson.M, len(f.Filters))
	for i, ft := range f.Filters {
		and[i] = filterToBson(ft)
	}
	return bson.M{"$and": and}
}

// likeToRegex 将 SQL LIKE 模式（% 任意串、_ 单字符）转为 Mongo $regex 模式，
// 同时转义正则特殊字符，避免用户输入中的元字符破坏匹配（BUG-041 修复）。
func likeToRegex(pattern string) string {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteString(".")
		default:
			if strings.ContainsRune(".+*?[]{}()|^$\\", r) {
				b.WriteString("\\")
			}
			b.WriteRune(r)
		}
	}
	b.WriteString("$")
	return b.String()
}

// filterToBson 将单个 Filter 转为 MongoDB bson 查询条件。
func filterToBson(f Filter) bson.M {
	switch f.Op {
	case OpEQ:
		return bson.M{f.Field: f.Value}
	case OpNEQ:
		return bson.M{f.Field: bson.M{"$ne": f.Value}}
	case OpLike:
		// Mongo 不认 SQL LIKE 的 % _ 通配符，需先转成 regex 模式（BUG-041 修复）
		return bson.M{f.Field: bson.M{"$regex": likeToRegex(fmt.Sprint(f.Value)), "$options": "i"}}
	case OpGT:
		return bson.M{f.Field: bson.M{"$gt": f.Value}}
	case OpGTE:
		return bson.M{f.Field: bson.M{"$gte": f.Value}}
	case OpLT:
		return bson.M{f.Field: bson.M{"$lt": f.Value}}
	case OpLTE:
		return bson.M{f.Field: bson.M{"$lte": f.Value}}
	case OpIn:
		return bson.M{f.Field: bson.M{"$in": f.Value}}
	case OpRange:
		return bson.M{f.Field: bson.M{"$gte": f.Value, "$lte": f.Value}}
	case "or_group":
		// OR 组：子 filter 之间用 $or 连接
		subs, _ := f.Value.([]Filter)
		if len(subs) > 0 {
			ors := make([]bson.M, len(subs))
			for i, sub := range subs {
				ors[i] = filterToBson(sub)
			}
			return bson.M{"$or": ors}
		}
		return bson.M{}
	case OpRaw:
		// 尝试将 "col1 = ? AND col2 = ?" 格式转为 bson （如草稿可见性过滤）
		switch v := f.Value.(type) {
		case []any:
			if len(v) >= 2 {
				if cond, ok := v[0].(string); ok {
					parts := strings.Split(cond, " AND ")
					if len(parts) > 1 && len(parts) == len(v)-1 {
						ands := make([]bson.M, len(parts))
						for i, part := range parts {
							part = strings.TrimSpace(part)
							if idx := strings.Index(part, " = ?"); idx > 0 {
								ands[i] = bson.M{part[:idx]: v[i+1]}
							}
						}
						return bson.M{"$and": ands}
					}
				}
			}
		}
		// 无法解析，返回空条件（向后兼容）
		return bson.M{}
	default:
		return bson.M{f.Field: f.Value}
	}
}

// ---------- 辅助 ----------

// toBsonDoc 将 struct 转为 bson.D（BSON 文档）。
// 支持 bson:",inline"（mongo-driver 语义）标记的匿名嵌入 struct 递归展开。
func toBsonDoc[M any](r *MongoCRUDRepository[M], entity *M) bson.D {
	v := reflect.ValueOf(entity)
	for v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	return toBsonDocFields(v)
}

// toBsonDocFields 递归遍历 struct 字段生成 bson.D。
// 匿名嵌入 struct 必须显式标记 bson:",inline"（或 bson:",inline,omitempty"）才会展开——
// 与 mongo-driver 语义一致，避免无标记匿名字段被隐式写入导致行为变化（P3 增强建议）。
// 展开的子字段遵循与顶层一致的 bson tag 规则：无 tag / "-" 跳过，带选项取逗号前段。
func toBsonDocFields(v reflect.Value) bson.D {
	t := v.Type()
	doc := make(bson.D, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("bson")
		if tag == "" || tag == "-" {
			continue
		}
		// P3：bson:",inline" 标记的匿名嵌入 struct → 递归展开子字段
		if f.Anonymous && f.Type.Kind() == reflect.Struct && isBsonInline(tag) {
			doc = append(doc, toBsonDocFields(v.Field(i))...)
			continue
		}
		// BUG-053：bson tag 可能带选项（如 bson:"link_url,omitempty"），
		// 只能取逗号前段作为字段 key——否则落库 key 变 "link_url,omitempty"（带逗号），
		// 读取时 mongo-driver 按标准解析取 link_url，字段读不到（create 后全空）。
		key := tag
		if i := strings.IndexByte(tag, ','); i >= 0 {
			key = tag[:i]
		}
		if key == "" { // 防御：bson:",omitempty" 之类无 key 的非法 tag 不写空 key
			continue
		}
		doc = append(doc, bson.E{Key: key, Value: v.Field(i).Interface()})
	}
	return doc
}

// isBsonInline 判断 bson tag 是否为 inline 选项（mongo-driver 语义）：
// 首段（key）必须为空，后续选项段含 "inline"（如 bson:",inline" / bson:",inline,omitempty"）。
// 注意 bson:"inline"（无逗号）首段是字段名，不算 inline 选项。
func isBsonInline(tag string) bool {
	parts := strings.Split(tag, ",")
	if parts[0] != "" {
		return false
	}
	for _, opt := range parts[1:] {
		if opt == "inline" {
			return true
		}
	}
	return false
}

// extractPKVal 从 struct 提取主键值（支持匿名嵌入 struct 递归查找）。
func extractPKVal(entity any, pkField string) any {
	v := reflect.ValueOf(entity)
	for v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	return extractPKValFields(v, pkField)
}

// extractPKValFields 递归查找主键字段值（P3：匿名嵌入 struct 递归，与 ensureAuditTime 一致）。
func extractPKValFields(v reflect.Value, pkField string) any {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" { // 未导出字段跳过（避免 Interface() panic）
			continue
		}
		tag := f.Tag.Get("bson")
		// BUG-053：与 toBsonDoc 一致，bson tag 只取逗号前段（去 omitempty 等选项）。
		if idx := strings.IndexByte(tag, ','); idx >= 0 {
			tag = tag[:idx]
		}
		if tag == pkField || (tag == "" && f.Name == pkField) {
			return v.Field(i).Interface()
		}
		colTag := f.Tag.Get("gorm")
		if strings.Contains(colTag, "column:"+pkField) {
			return v.Field(i).Interface()
		}
		// 匿名嵌入 struct → 递归查找
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			if val := extractPKValFields(v.Field(i), pkField); val != nil {
				return val
			}
		}
	}
	return nil
}

// detectPK 从 bson 标签自动推导主键（支持匿名嵌入 struct 递归查找）。
func (r *MongoCRUDRepository[M]) detectPK() {
	var m M
	v := reflect.ValueOf(m)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return
	}
	if col := findPKField(v); col != "" {
		r.pkField = col
		return
	}
	r.pkField = "_id"
}

// findPKField 递归查找主键列名：优先 bson:"_id"，其次 GORM primaryKey → column。
func findPKField(v reflect.Value) string {
	t := v.Type()
	// 第一轮：bson:"_id"
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		if tag := f.Tag.Get("bson"); tag == "_id" {
			return "_id"
		}
	}
	// 第二轮：匿名嵌入 struct 递归 + GORM primaryKey → extract column
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			if col := findPKField(v.Field(i)); col != "" {
				return col
			}
		}
		tag := f.Tag.Get("gorm")
		if strings.Contains(tag, "primaryKey") {
			col := common.ExtractGormColumn(f.Tag.Get("gorm"))
			if col != "" {
				return col
			}
		}
	}
	return ""
}

// toStructSlice 泛型类型反射 helper（预留）
func toStructSlice(v any) reflect.Value { return reflect.ValueOf(v) }
