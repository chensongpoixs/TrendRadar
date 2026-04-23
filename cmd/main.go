package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/kardianos/service"
)

// program 实现 kardianos/service.Interface，用于 Windows 服务 / Linux systemd 等同套生命周期。
type program struct {
	cancel  context.CancelFunc
	runDone chan struct{}
}

func (p *program) Start(s service.Service) error {
	p.runDone = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go func() {
		defer close(p.runDone)
		if err := runApp(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "trendradar runApp: %v\n", err)
			os.Exit(1)
		}
	}()
	return nil
}

func (p *program) Stop(s service.Service) error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.runDone != nil {
		select {
		case <-p.runDone:
		case <-time.After(25 * time.Second):
		}
	}
	return nil
}

func buildServiceConfig(exeDir string) *service.Config {
	return &service.Config{
		Name:        "TrendRadar",
		DisplayName: "TrendRadar 趋势雷达",
		Description: "热点与 RSS 抓取、AI 过滤、HTTP API（backend-go）",
		// Linux systemd：工作目录；Windows 不生效，已依赖 chdirToExecutable
		WorkingDirectory: exeDir,
	}
}

func main() {
	exeDir, err := chdirToExecutable()
	if err != nil {
		log.Fatalf("chdir to executable: %v", err)
	}

	// 子命令：服务安装/卸载/启停（与 sc 对应能力；Windows 建议管理员执行 install）
	if len(os.Args) > 1 {
		if os.Args[1] == "help" || os.Args[1] == "-h" || os.Args[1] == "--help" {
			fmt.Fprintln(os.Stdout, `用法:
  trendradar              以前台方式运行（开发/调试用）
  trendradar install      向系统注册为服务（需管理员 / root）
  trendradar uninstall    从系统移除服务
  trendradar start|stop|restart  通过服务管理器启停
详见 docs/service-windows-linux.md`)
			return
		}
	}

	prg := &program{}
	svc, err := service.New(prg, buildServiceConfig(exeDir))
	if err != nil {
		log.Fatalf("service: %v", err)
	}

	if len(os.Args) > 1 {
		a := os.Args[1]
		if err := service.Control(svc, a); err != nil {
			log.Fatalf("service %s: %v", a, err)
		}
		return
	}

	// 前台：与 kardianos 统一走 Run（控制台 Ctrl+C 会调 Stop）
	if err = svc.Run(); err != nil {
		log.Fatalf("Run: %v", err)
	}
}
