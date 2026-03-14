package logger

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	instance *log.Logger
	file     *os.File
	once     sync.Once
	logPath  string
)

func Init(configDir string) error {
	var initErr error
	once = sync.Once{}
	once.Do(func() {
		logDir := filepath.Join(configDir, "logs")
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			initErr = err
			return
		}

		logPath = filepath.Join(logDir, time.Now().Format("2006-01-02")+".log")
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			initErr = err
			return
		}
		file = f
		instance = log.New(f, "", log.Ldate|log.Ltime|log.Lmicroseconds)
	})
	return initErr
}

func Info(format string, args ...interface{}) {
	write("INFO", format, args...)
}

func Error(format string, args ...interface{}) {
	write("ERROR", format, args...)
}

func Debug(format string, args ...interface{}) {
	write("DEBUG", format, args...)
}

func write(level, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if instance != nil {
		instance.Printf("[%s] %s", level, msg)
	}
}

func Path() string {
	return logPath
}

func Close() {
	if file != nil {
		file.Close()
	}
}
