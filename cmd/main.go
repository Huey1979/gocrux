package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Huey1979/gocrux/internal/bootstrap"
	"github.com/Huey1979/gocrux/internal/config"
	"github.com/Huey1979/gocrux/internal/logger"
	"github.com/Huey1979/gocrux/internal/router"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

func main() {
	// 1. 加载配置
	if _, err := config.Load("config.yaml"); err != nil {
		fmt.Printf("加载配置失败: %v\n", err)
		os.Exit(1)
	}

	// 2. 初始化日志
	if err := logger.Init("./logs"); err != nil {
		fmt.Printf("初始化日志失败: %v\n", err)
		os.Exit(1)
	}

	// 3. 初始化所有组件
	if err := bootstrap.Init(); err != nil {
		logrus.Fatalf("初始化失败: %v", err)
	}

	// 4. 数据库迁移（使用者传入模型）
	// bootstrap.Migrate(&YourModel{}, &AnotherModel{})

	// 5. 启动 HTTP 服务
	if config.Cfg.App.Mode == "debug" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}
	engine := gin.New()
	router.Setup(engine)

	addr := fmt.Sprintf("%s:%d", config.Cfg.App.Host, config.Cfg.App.Port)
	logrus.Infof("启动 HTTP 服务: %s", addr)

	go func() {
		if err := engine.Run(addr); err != nil {
			logrus.Fatalf("HTTP 服务启动失败: %v", err)
		}
	}()

	// 6. 等待退出信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	logrus.Info("正在关闭服务...")
	if err := bootstrap.Close(); err != nil {
		logrus.Errorf("关闭失败: %v", err)
	}
}
