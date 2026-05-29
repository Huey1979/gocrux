package mongodb

import (
	"context"
	"time"

	"github.com/Huey1979/gocrux/internal/config"

	errs "github.com/Huey1979/gocrux/errors"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

var Client *mongo.Client
var Database *mongo.Database

// Init 初始化 MongoDB 连接
func Init(cfg *config.MongoDBConfig) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 配置客户端选项
	clientOpts := options.Client().
		ApplyURI(cfg.URI()).
		SetMinPoolSize(uint64(cfg.MinPoolSize)).
		SetMaxPoolSize(uint64(cfg.MaxPoolSize))

	// 连接 MongoDB
	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		return errs.ErrMongoDBConnect(err)
	}

	// 测试连接
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		return errs.ErrMongoDBPing(err)
	}

	Client = client
	Database = client.Database(cfg.Database)

	return nil
}

// Close 关闭 MongoDB 连接
func Close() error {
	if Client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return Client.Disconnect(ctx)
	}
	return nil
}

// GetCollection 获取集合
func GetCollection(name string) *mongo.Collection {
	return Database.Collection(name)
}

// Collection 业务表集合名称常量
var Collection = struct {
	// 实体业务数据
	EntityData string // 实体数据

	// 流程业务数据
	FlowInstance    string // 流程实例
	FlowTask        string // 流程任务
	ApprovalRecord  string // 审批记录
	ApprovalComment string // 审批评论

	// 辅助功能
	NotifyContent            string // 通知内容
	NotifyNotification       string // 通知
	DiscussionGroup          string // 讨论组
	DiscussionMember         string // 讨论组成员
	DiscussionGroupMessage   string // 群消息
	DiscussionPrivateMessage string // 私聊消息

	// 定时任务
	SchedEvent       string // 日程事件
	SchedParticipant string // 日程参与人
	SchedReminder    string // 日程提醒
}{
	// 实体业务数据
	EntityData: "entity_data",

	// 流程业务数据
	FlowInstance:    "flow_instances",
	FlowTask:        "flow_tasks",
	ApprovalRecord:  "approval_records",
	ApprovalComment: "approval_comments",

	// 辅助功能
	NotifyContent:            "notify_content",
	NotifyNotification:       "notify_notification",
	DiscussionGroup:          "discussion_group",
	DiscussionMember:         "discussion_member",
	DiscussionGroupMessage:   "discussion_group_message",
	DiscussionPrivateMessage: "discussion_private_message",

	// 定时任务
	SchedEvent:       "sched_event",
	SchedParticipant: "sched_participant",
	SchedReminder:    "sched_reminder",
}
