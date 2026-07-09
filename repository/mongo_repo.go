package repository

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/Huey1979/gocrux/common"
	"github.com/Huey1979/gocrux/internal/database/mongodb"

	"go.mongodb.org/mongo-driver/bson"
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

// BatchDeprecateVersions 版本化批量废弃：将当前版本标记为非当前（isCurrent=false, versionStatus=deprecated）。
// 供 Service._doDelete 在 VersionMode=true 时调用。
func (r *MongoCRUDRepository[M]) BatchDeprecateVersions(ctx context.Context, ids []any) error {
	_, err := r.coll.UpdateMany(ctx, bson.M{r.pkField: bson.M{"$in": ids}},
		bson.M{"$set": bson.M{"isCurrent": false, "versionStatus": "deprecated"}})
	return err
}

// BatchDeprecateVersionsByFK 版本化按外键批量废弃：级联删除子记录时使用。
func (r *MongoCRUDRepository[M]) BatchDeprecateVersionsByFK(ctx context.Context, fkField string, fkValues []any) error {
	_, err := r.coll.UpdateMany(ctx, bson.M{fkField: bson.M{"$in": fkValues}},
		bson.M{"$set": bson.M{"isCurrent": false, "versionStatus": "deprecated"}})
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
func (r *MongoCRUDRepository[M]) Insert(ctx context.Context, entity *M) error {
	data := toBsonDoc(r, entity)
	if _, err := r.coll.InsertOne(ctx, data); err != nil {
		return fmt.Errorf("MongoDB插入失败: %w", err)
	}
	return nil
}

// InsertBatch 批量插入。
func (r *MongoCRUDRepository[M]) InsertBatch(ctx context.Context, entities []*M) error {
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

// List 分页列表查询。
func (r *MongoCRUDRepository[M]) List(ctx context.Context, filter bson.M, page, pageSize int) ([]M, int64, error) {
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
	skip := int64((page - 1) * pageSize)
	opts := options.Find().SetSkip(skip).SetLimit(int64(pageSize))
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

// ListByFilters 结构化过滤查询（将 Filter 转换为 bson）。
func (r *MongoCRUDRepository[M]) ListByFilters(ctx context.Context, filters ListFilters) ([]M, int64, error) {
	f := toBsonFilter(filters)
	return r.List(ctx, f, filters.Page, filters.PageSize)
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

// BatchSoftDelete 批量软删除。
func (r *MongoCRUDRepository[M]) BatchSoftDelete(ctx context.Context, ids []any) error {
	_, err := r.coll.UpdateMany(ctx, bson.M{r.pkField: bson.M{"$in": ids}}, bson.M{"$set": bson.M{"isDeleted": int8(1)}})
	return err
}

// BatchSoftDeleteByFK 按外键批量软删除。
func (r *MongoCRUDRepository[M]) BatchSoftDeleteByFK(ctx context.Context, fkField string, fkValues []any) error {
	_, err := r.coll.UpdateMany(ctx, bson.M{fkField: bson.M{"$in": fkValues}}, bson.M{"$set": bson.M{"isDeleted": int8(1)}})
	return err
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

// filterToBson 将单个 Filter 转为 MongoDB bson 查询条件。
func filterToBson(f Filter) bson.M {
	switch f.Op {
	case OpEQ:
		return bson.M{f.Field: f.Value}
	case OpNEQ:
		return bson.M{f.Field: bson.M{"$ne": f.Value}}
	case OpLike:
		return bson.M{f.Field: bson.M{"$regex": f.Value, "$options": "i"}}
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
func toBsonDoc[M any](r *MongoCRUDRepository[M], entity *M) bson.D {
	v := reflect.ValueOf(entity)
	for v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
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
		doc = append(doc, bson.E{Key: tag, Value: v.Field(i).Interface()})
	}
	return doc
}

// extractPKVal 从 struct 提取主键值。
func extractPKVal(entity any, pkField string) any {
	v := reflect.ValueOf(entity)
	for v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("bson")
		if tag == pkField || (tag == "" && t.Field(i).Name == pkField) {
			return v.Field(i).Interface()
		}
		colTag := t.Field(i).Tag.Get("gorm")
		if strings.Contains(colTag, "column:"+pkField) {
			return v.Field(i).Interface()
		}
	}
	return nil
}

// detectPK 从 bson 标签自动推导主键。
func (r *MongoCRUDRepository[M]) detectPK() {
	var m M
	v := reflect.ValueOf(m)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if tag := t.Field(i).Tag.Get("bson"); tag == "_id" {
			r.pkField = "_id"
			return
		}
	}
	// fallback: GORM primaryKey → extract column
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("gorm")
		if strings.Contains(tag, "primaryKey") {
			col := common.ExtractGormColumn(t.Field(i).Tag.Get("gorm"))
			if col != "" {
				r.pkField = col
				return
			}
		}
	}
	r.pkField = "_id"
}

// toStructSlice 泛型类型反射 helper（预留）
func toStructSlice(v any) reflect.Value { return reflect.ValueOf(v) }
