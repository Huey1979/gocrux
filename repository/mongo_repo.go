package repository

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/Huey1979/gocrux/internal/database/mongodb"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ============================================================
// MongoCRUDRepository MongoDB 泛型仓储
//
// 提供与 CRUDRepository 一致的 CRUD 接口，但底层使用 MongoDB。
// 用法：
//
//	repo := NewMongoCRUDRepository[entity.Product]("products")
//	product, err := repo.GetByID(ctx, "01Jxxx...")
// ============================================================

type MongoCRUDRepository[M any] struct {
	coll    *mongo.Collection
	pkField string
}

// NewMongoCRUDRepository 创建 MongoDB 泛型仓储。
// collectionName: MongoDB 集合名。
func NewMongoCRUDRepository[M any](collectionName string) *MongoCRUDRepository[M] {
	r := &MongoCRUDRepository[M]{
		coll:    mongodb.Database.Collection(collectionName),
		pkField: "_id",
	}
	r.detectPK()
	return r
}

// SetColl 注入自定义 Collection（测试用）。
func (r *MongoCRUDRepository[M]) SetColl(coll *mongo.Collection) *MongoCRUDRepository[M] {
	r.coll = coll
	return r
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
	if err := r.coll.FindOne(ctx, filter).Decode(&result); err != nil {
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
	if err := r.coll.FindOne(ctx, filter).Decode(&result); err != nil {
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
	total, err := r.coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("MongoDB计数失败: %w", err)
	}
	skip := int64((page - 1) * pageSize)
	opts := options.Find().SetSkip(skip).SetLimit(int64(pageSize))
	cursor, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, fmt.Errorf("MongoDB查询失败: %w", err)
	}
	defer cursor.Close(ctx)
	var results []M
	if err := cursor.All(ctx, &results); err != nil {
		return nil, 0, fmt.Errorf("MongoDB读取失败: %w", err)
	}
	return results, total, nil
}

// ListAll 全量查询。
func (r *MongoCRUDRepository[M]) ListAll(ctx context.Context) ([]M, error) {
	cursor, err := r.coll.Find(ctx, bson.M{})
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
	cursor, err := r.coll.Find(ctx, bson.M{field: value})
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

// ---------- 辅助 ----------

// toBsonDoc 将 struct 转为 bson.D（BSON 文档）。
func toBsonDoc[M any](r *MongoCRUDRepository[M], entity *M) bson.D {
	v := reflect.ValueOf(entity)
	if v.Kind() == reflect.Ptr {
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
	if v.Kind() == reflect.Ptr {
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
			col := extractGormColumn(t.Field(i).Tag.Get("gorm"))
			if col != "" {
				r.pkField = col
				return
			}
		}
	}
	r.pkField = "_id"
}

// extractGormColumn 从 gorm tag 提取 column:xxx 的值
func extractGormColumn(gormTag string) string {
	for _, part := range strings.Split(gormTag, ";") {
		if strings.HasPrefix(part, "column:") {
			return part[7:]
		}
	}
	return ""
}

// toStructSlice 泛型类型反射 helper（预留）
func toStructSlice(v any) reflect.Value { return reflect.ValueOf(v) }
