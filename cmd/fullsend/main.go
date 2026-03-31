// Package main is the entry point for the fullsend CLI tool.
//
// fullsend automates the onboarding and operation of autonomous agentic
// development pipelines for GitHub-hosted organizations.
package main

import (
	"os"

	"github.com/fullsend-ai/fullsend/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
