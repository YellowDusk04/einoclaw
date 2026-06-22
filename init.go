package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/cloudwego/eino/adk"
	"gopkg.in/yaml.v3"
)

func init() {
	var err error

	// init homeDir
	homeDir, err = os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}

	// init rootDir
	rootDir = filepath.Join(homeDir, ".einoclaw")
	ensureDir(rootDir)

	// init sessionDir
	sessionDir = filepath.Join(rootDir, "sessions")
	ensureDir(sessionDir)

	// init memoryDir
	memoryDir = filepath.Join(rootDir, "memory")
	ensureDir(memoryDir)

	// set log output to file
	logDir := filepath.Join(rootDir, "log")
	ensureDir(logDir)
	logFile, err = os.OpenFile(filepath.Join(logDir, time.Now().Format("2006-01-02")+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		panic(err)
	}

	log.SetOutput(logFile)
	log.SetFlags(log.Ltime | log.Lshortfile)

	// init config file
	configPath := filepath.Join(rootDir, "config.yaml")
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		// create default config file, then exit
		data, err := os.ReadFile("example.yaml")
		if err != nil {
			log.Fatal(err)
		}
		err = os.WriteFile(configPath, data, 0644)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(`✅ 已创建默认配置文件: ~/.einoclaw/config.yaml
请填入模型相关配置后重启程序`)
		os.Exit(0)
	} else if err != nil {
		log.Fatal(err)
	}

	// load config file
	data, err := os.ReadFile(configPath)
	if err != nil {
		log.Fatal(err)
	}
	cfg = &config{}
	err = yaml.Unmarshal(data, cfg)
	if err != nil {
		log.Fatal(err)
	}
	if len(cfg.Models) == 0 {
		fmt.Println(`⚠️ 请在 ~/.einoclaw/config.yaml 中填入模型相关配置后重启程序`)
	}

	// init sessionID
	sessionID = fmt.Sprintf("%d", time.Now().Unix())

	// set eino language to chinese
	adk.SetLanguage(adk.LanguageChinese)

	// default to use model[0]
	loadModelAndAgent(0)
}

func ensureDir(dir string) {
	err := os.MkdirAll(dir, 0755)
	if err != nil {
		log.Fatal(err)
	}
}
