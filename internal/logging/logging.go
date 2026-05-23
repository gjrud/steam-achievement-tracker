package logging

import (
	"log"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

func Init(path string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		_ = os.Chmod(filepath.Dir(path), 0o700)
	}
	if file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_ = file.Close()
		_ = os.Chmod(path, 0o600)
	}
	log.SetOutput(&lumberjack.Logger{
		Filename:   path,
		MaxSize:    5,
		MaxBackups: 5,
		Compress:   false,
	})
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)
	log.Printf("logging initialized")
}

func Fallback() {
	log.SetOutput(os.Stderr)
}
