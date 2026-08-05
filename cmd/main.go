package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/jrmarcco/goaw/internal/engine"
	mockprovider "github.com/jrmarcco/goaw/internal/provider/mock"
	mockregistry "github.com/jrmarcco/goaw/internal/tools/mock"
	"go.uber.org/zap"
)

func main() {
	fmt.Println("🚀 Welcome to goaw!")

	workDir, _ := os.Getwd()

	provider := mockprovider.NewMockProvider()
	registry := mockregistry.NewMockRegistry()

	logger, _ := zap.NewDevelopment()

	ae, _ := engine.NewAgentEngine(workDir, provider, registry, logger)

	err := ae.Run(context.Background(), "I want to check the files in the current directory.")
	if err != nil {
		log.Fatalf("engine crash: %v", err)
	}
}
