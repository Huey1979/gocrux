package config

import (
	"fmt"
	"os"

	errs "github.com/Huey1979/gocrux/errors"
	"gopkg.in/yaml.v3"
)

var Cfg *Config

type Config struct {
	App      AppConfig      `yaml:"app"`
	MySQL    MySQLConfig    `yaml:"mysql"`
	MongoDB  MongoDBConfig  `yaml:"mongodb"`
	Redis    RedisConfig    `yaml:"redis"`
	Log      LogConfig      `yaml:"log"`
	Security SecurityConfig `yaml:"security"`
	Storage  StorageConfig  `yaml:"storage"`
}

type AppConfig struct {
	Name string `yaml:"name"`
	Mode string `yaml:"mode"` // debug, release
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type MySQLConfig struct {
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	User         string `yaml:"user"`
	Password     string `yaml:"password"`
	Database     string `yaml:"database"`
	Charset      string `yaml:"charset"`
	MaxOpenConns int    `yaml:"max_open_conns"`
	MaxIdleConns int    `yaml:"max_idle_conns"`
	MaxLifeTime  int    `yaml:"max_life_time"`
}

func (m MySQLConfig) DSN() string {
	charset := m.Charset
	if charset == "" {
		charset = "utf8mb4"
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=True&loc=Local",
		m.User, m.Password, m.Host, m.Port, m.Database, charset)
}

type MongoDBConfig struct {
	Hosts       []string `yaml:"hosts"`
	Database    string   `yaml:"database"`
	Username    string   `yaml:"username"`
	Password    string   `yaml:"password"`
	MinPoolSize int      `yaml:"min_pool_size"`
	MaxPoolSize int      `yaml:"max_pool_size"`
}

func (m MongoDBConfig) URI() string {
	if m.Username != "" && m.Password != "" {
		return fmt.Sprintf("mongodb://%s:%s@%s/%s?minPoolSize=%d&maxPoolSize=%d",
			m.Username, m.Password, m.Hosts[0], m.Database, m.MinPoolSize, m.MaxPoolSize)
	}
	return fmt.Sprintf("mongodb://%s/%s?minPoolSize=%d&maxPoolSize=%d",
		m.Hosts[0], m.Database, m.MinPoolSize, m.MaxPoolSize)
}

type RedisConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	PoolSize int    `yaml:"pool_size"`
}

func (r RedisConfig) Addr() string {
	return fmt.Sprintf("%s:%d", r.Host, r.Port)
}

type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
	Output string `yaml:"output"`
	File   struct {
		Path       string `yaml:"path"`
		MaxSize    int    `yaml:"max_size"`
		MaxBackups int    `yaml:"max_backups"`
		MaxAge     int    `yaml:"max_age"`
		Compress   bool   `yaml:"compress"`
	} `yaml:"file"`
}

type SecurityConfig struct {
	JWTSecret string `yaml:"jwt_secret"`
	JWTExpire int    `yaml:"jwt_expire"`
	Salt      string `yaml:"salt"`
}

type StorageConfig struct {
	Type  string `yaml:"type"` // local, oss, s3
	Local struct {
		BasePath string `yaml:"base_path"`
		BaseURL  string `yaml:"base_url"`
	} `yaml:"local"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.ErrConfigRead(err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, errs.ErrConfigParse(err)
	}

	Cfg = &cfg
	return &cfg, nil
}
