package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// MongoDBConfig.URI 的构造：replicaSet 只在配置非空时附加，其余参数保持原样。
func TestMongoDBConfigURI(t *testing.T) {
	base := MongoDBConfig{
		Hosts:       []string{"localhost:27017"},
		Database:    "gocrux",
		MinPoolSize: 10,
		MaxPoolSize: 100,
	}

	t.Run("单机（未配副本集）不改道", func(t *testing.T) {
		got := base.URI()
		want := "mongodb://localhost:27017/gocrux?minPoolSize=10&maxPoolSize=100"
		if got != want {
			t.Fatalf("URI()=%q want %q", got, want)
		}
	})

	t.Run("配副本集时附加 replicaSet", func(t *testing.T) {
		cfg := base
		cfg.ReplicaSet = "rs0"
		got := cfg.URI()
		want := "mongodb://localhost:27017/gocrux?minPoolSize=10&maxPoolSize=100&replicaSet=rs0"
		if got != want {
			t.Fatalf("URI()=%q want %q", got, want)
		}
	})

	t.Run("带账号密码", func(t *testing.T) {
		cfg := base
		cfg.Username, cfg.Password, cfg.ReplicaSet = "root", "pwd", "rs0"
		got := cfg.URI()
		want := "mongodb://root:pwd@localhost:27017/gocrux?minPoolSize=10&maxPoolSize=100&replicaSet=rs0"
		if got != want {
			t.Fatalf("URI()=%q want %q", got, want)
		}
	})

	t.Run("副本集名做 URL 转义", func(t *testing.T) {
		cfg := base
		cfg.ReplicaSet = "rs#1"
		got := cfg.URI()
		if !strings.Contains(got, "replicaSet=rs%231") {
			t.Fatalf("副本集名应转义后写入查询串: %q", got)
		}
		if strings.Contains(got, "rs#1") {
			t.Fatalf("副本集名不应原样保留特殊字符: %q", got)
		}
	})
}

// yaml 键为 replica_set（与 config.yaml 一致），空值不附加参数。
func TestMongoDBConfigYAMLReplicaSet(t *testing.T) {
	var cfg Config
	raw := `
mongodb:
  hosts:
    - db1:27017
  database: gocrux
  username: ""
  password: ""
  min_pool_size: 10
  max_pool_size: 100
  replica_set: rs0
`
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if cfg.MongoDB.ReplicaSet != "rs0" {
		t.Fatalf("replica_set 未解析到 ReplicaSet: %q", cfg.MongoDB.ReplicaSet)
	}
	if !strings.Contains(cfg.MongoDB.URI(), "&replicaSet=rs0") {
		t.Fatalf("连接串应含 replicaSet 参数: %q", cfg.MongoDB.URI())
	}

	// 未配置（缺省）时不附加，保持既有行为
	var plain Config
	if err := yaml.Unmarshal([]byte("mongodb:\n  hosts: [\"db1:27017\"]\n  database: gocrux\n"), &plain); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if strings.Contains(plain.MongoDB.URI(), "replicaSet") {
		t.Fatalf("未配置 replica_set 时不应附加参数: %q", plain.MongoDB.URI())
	}
}
