package entity

import "time"

// SysOperationLog 操作日志（框架内置审计日志）
type SysOperationLog struct {
	LogULID      string    `gorm:"column:log_ulid;type:varchar(26);primaryKey" json:"log_ulid"`
	EntityType   string    `gorm:"column:entity_type;type:varchar(100);index" json:"entity_type"`
	EntityID     string    `gorm:"column:entity_id;type:varchar(26);index" json:"entity_id"`
	Operation    string    `gorm:"column:operation;type:varchar(50)" json:"operation"`
	OperatorULID string    `gorm:"column:operator_ulid;type:varchar(26)" json:"operator_ulid"`
	RequestID    string    `gorm:"column:request_id;type:varchar(64)" json:"request_id"`
	OperatedAt   time.Time `gorm:"column:operated_at" json:"operated_at"`
}

func (SysOperationLog) TableName() string {
	return "sys_operation_log"
}
