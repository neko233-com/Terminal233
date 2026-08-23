package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/neko233-com/Terminal233/terminal233-server/internal/app"
)

func main() {
	showVersion := flag.Bool("version", false, "print the Terminal233 server version")
	flag.Parse()
	if *showVersion {
		fmt.Printf("Terminal233 Server %s\n", app.Version)
		return
	}

	cfg, err := app.ConfigFromEnv()
	if err != nil {
		log.Fatalf("Terminal233 Server 配置错误：%v", err)
	}
	server, err := app.New(cfg)
	if err != nil {
		log.Fatalf("Terminal233 Server 初始化失败：%v", err)
	}

	httpServer := &http.Server{
		Addr:              cfg.Address,
		Handler:           server,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("Terminal233 Server listening on %s", cfg.Address)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("server stopped: %v", err)
		os.Exit(1)
	}
}
