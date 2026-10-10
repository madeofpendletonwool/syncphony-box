package config

import (
	"log"
	"os"
	"testing"
)

func testLogger() *log.Logger {
	if testing.Verbose() {
		return log.New(os.Stderr, "", log.LstdFlags)
	}
	return log.New(log.Writer(), "", 0)
}
