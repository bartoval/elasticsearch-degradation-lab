package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/bartoval/elasticsearch-degradation-lab/internal/elasticsearch"
)

func main() {
	started := time.Now()

	installPath, installError := elasticsearch.EnsureDistribution(
		context.Background(),
		os.Args[1],
		os.Args[2],
		os.Args[3],
		func(message string) {
			fmt.Println("[progress]", message, time.Since(started).Round(time.Second))
		},
	)
	if installError != nil {
		fmt.Println("FAILED:", installError)
		os.Exit(1)
	}

	fmt.Println("OK:", installPath, "in", time.Since(started).Round(time.Second))
}
