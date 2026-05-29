package repository

import (
	"context"
	"time"

	"github.com/Huey1979/gocrux/common"
	"github.com/Huey1979/gocrux/internal/database/mysql"
)

// ========== 通用仓储接口 ==========

type Repository interface {
	Create(ctx context.Context, ulid string, data interface{}) error
	GetByULID(ctx context.Context, ulid string) (interface{}, error)
	Update(ctx context.Context, ulid string, data interface{}) error
	Delete(ctx context.Context, ulid string) error
	List(ctx context.Context, conditions interface{}) ([]interface{}, int64, error)
	Count(ctx context.Context, conditions interface{}) (int64, error)
}

type BaseRepository struct {
	TableName string
	ULIDField string
}

func NewBaseRepository(tableName, ulidField string) *BaseRepository {
	return &BaseRepository{TableName: tableName, ULIDField: ulidField}
}

// ========== 版本管理 ==========

type VersionConfig struct {
	TableName        string
	ULIDField        string
	CodeField        string
	NameField        string
	CurrentField     string
	StatusField      string
	RemarkField      string
	ParentField      string
	VersionField     string
	PublishedAtField string
	PublishedByField string
}

type VersionRepository struct {
	config VersionConfig
}

func NewVersionRepository(config VersionConfig) *VersionRepository {
	if config.CurrentField == "" {
		config.CurrentField = "is_current"
	}
	if config.StatusField == "" {
		config.StatusField = "version_status"
	}
	if config.RemarkField == "" {
		config.RemarkField = "version_remark"
	}
	if config.ParentField == "" {
		config.ParentField = "parent_ulid"
	}
	if config.VersionField == "" {
		config.VersionField = "version_code"
	}
	if config.PublishedAtField == "" {
		config.PublishedAtField = "published_at"
	}
	if config.PublishedByField == "" {
		config.PublishedByField = "published_by"
	}
	return &VersionRepository{config: config}
}

func (r *VersionRepository) table() string { return r.config.TableName }

func (r *VersionRepository) GetCurrentVersion(ctx context.Context, code string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := mysql.DB.WithCtx(ctx).Table(r.table()).
		Where(r.config.CodeField+" = ? AND "+r.config.CurrentField+" = 1 AND is_deleted = 0", code).
		First(&result).Error
	return result, err
}

func (r *VersionRepository) GetVersionByULID(ctx context.Context, ulid string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := mysql.DB.WithCtx(ctx).Table(r.table()).
		Where(r.config.ULIDField+" = ? AND is_deleted = 0", ulid).
		First(&result).Error
	return result, err
}

func (r *VersionRepository) ListVersions(ctx context.Context, code string) ([]map[string]interface{}, error) {
	var results []map[string]interface{}
	err := mysql.DB.WithCtx(ctx).Table(r.table()).
		Where(r.config.CodeField+" = ? AND is_deleted = 0", code).
		Order("created_at DESC").
		Find(&results).Error
	return results, err
}

// ========== 版本操作 ==========

var (
	ErrCannotPublish          = &VersionError{Msg: "只能发布草稿状态的版本"}
	ErrCannotRollbackDeleted  = &VersionError{Msg: "已删除的版本不能回滚"}
	ErrCannotRollbackCurrent  = &VersionError{Msg: "当前版本不能回滚"}
	ErrTargetVersionNotFound  = &VersionError{Msg: "目标版本不存在"}
	ErrCurrentVersionNotFound = &VersionError{Msg: "当前版本不存在"}
)

type VersionError struct{ Msg string }

func (e *VersionError) Error() string { return e.Msg }

// CreateVersion 基于当前版本创建新草稿
func (r *VersionRepository) CreateVersion(ctx context.Context, code, name, remark, userULID string) (string, error) {
	current, err := r.GetCurrentVersion(ctx, code)
	if err != nil {
		return "", err
	}

	tx := mysql.DB.WithCtx(ctx).Begin()

	err = tx.Table(r.table()).
		Where(r.config.CodeField+" = ? AND "+r.config.CurrentField+" = 1", code).
		Update(r.config.CurrentField, 0).Error
	if err != nil {
		tx.Rollback()
		return "", err
	}

	newULID := common.NewULID()
	now := time.Now()
	newVersionCode := r.nextVersionCode(current[r.config.VersionField].(string))

	newData := make(map[string]interface{})
	for k, v := range current {
		switch k {
		case r.config.ULIDField:
			newData[k] = newULID
		case r.config.CurrentField:
			newData[k] = 1
		case r.config.ParentField:
			newData[k] = current[r.config.ULIDField]
		case r.config.VersionField:
			newData[k] = newVersionCode
		case r.config.StatusField:
			newData[k] = "draft"
		case r.config.RemarkField:
			newData[k] = remark
		case "created_by":
			newData[k] = userULID
		case "created_at":
			newData[k] = now
		case "updated_by":
			newData[k] = userULID
		case "updated_at":
			newData[k] = now
		default:
			newData[k] = v
		}
	}

	if err := tx.Table(r.table()).Create(newData).Error; err != nil {
		tx.Rollback()
		return "", err
	}

	tx.Commit()
	return newULID, nil
}

// PublishVersion 发布版本
func (r *VersionRepository) PublishVersion(ctx context.Context, ulid, userULID string) error {
	versionInfo, err := r.GetVersionByULID(ctx, ulid)
	if err != nil {
		return err
	}

	status := versionInfo[r.config.StatusField].(string)
	if status != "draft" {
		return ErrCannotPublish
	}

	code := versionInfo[r.config.CodeField].(string)
	tx := mysql.DB.WithCtx(ctx).Begin()

	err = tx.Table(r.table()).
		Where(r.config.CodeField+" = ? AND "+r.config.StatusField+" = 'published'", code).
		Update(r.config.StatusField, "deprecated").Error
	if err != nil {
		tx.Rollback()
		return err
	}

	now := time.Now()
	err = tx.Table(r.table()).
		Where(r.config.ULIDField+" = ?", ulid).
		Updates(map[string]interface{}{
			r.config.StatusField:      "published",
			r.config.PublishedAtField: now,
			r.config.PublishedByField: userULID,
		}).Error
	if err != nil {
		tx.Rollback()
		return err
	}

	tx.Commit()
	return nil
}

// RollbackVersion 回滚到指定版本
func (r *VersionRepository) RollbackVersion(ctx context.Context, targetULID, userULID string) error {
	target, err := r.GetVersionByULID(ctx, targetULID)
	if err != nil {
		return err
	}

	if target["is_deleted"] == int8(1) {
		return ErrCannotRollbackDeleted
	}
	if target[r.config.CurrentField] == int8(1) {
		return ErrCannotRollbackCurrent
	}

	code := target[r.config.CodeField].(string)
	current, err := r.GetCurrentVersion(ctx, code)
	if err != nil {
		return err
	}

	tx := mysql.DB.WithCtx(ctx).Begin()

	err = tx.Table(r.table()).
		Where(r.config.CodeField+" = ? AND "+r.config.CurrentField+" = 1", code).
		Update(r.config.CurrentField, 0).Error
	if err != nil {
		tx.Rollback()
		return err
	}

	newULID := common.NewULID()
	now := time.Now()
	newVersionCode := r.nextVersionCode(current[r.config.VersionField].(string))

	newData := make(map[string]interface{})
	for k, v := range target {
		switch k {
		case r.config.ULIDField:
			newData[k] = newULID
		case r.config.CurrentField:
			newData[k] = 1
		case r.config.ParentField:
			newData[k] = targetULID
		case r.config.VersionField:
			newData[k] = newVersionCode
		case r.config.StatusField:
			newData[k] = "published"
		case "created_by":
			newData[k] = userULID
		case "created_at":
			newData[k] = now
		case "updated_by":
			newData[k] = userULID
		case "updated_at":
			newData[k] = now
		default:
			newData[k] = v
		}
	}

	if err := tx.Table(r.table()).Create(newData).Error; err != nil {
		tx.Rollback()
		return err
	}

	err = tx.Table(r.table()).
		Where(r.config.CodeField+" = ? AND "+r.config.StatusField+" = 'published' AND "+r.config.CurrentField+" = 0", code).
		Update(r.config.StatusField, "deprecated").Error
	if err != nil {
		tx.Rollback()
		return err
	}

	tx.Commit()
	return nil
}

func (r *VersionRepository) nextVersionCode(current string) string {
	return common.NewULID()
}

func (r *VersionRepository) PublishHistory() {}
