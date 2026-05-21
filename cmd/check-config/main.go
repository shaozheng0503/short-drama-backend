package main

import (
	"flag"
	"fmt"
	"os"

	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/configcheck"
)

func main() {
	prod := flag.Bool("prod", false, "按生产上线标准检查配置")
	flag.Parse()

	report := configcheck.Run(config.Load(), configcheck.Options{Prod: *prod})
	printReport(report)
	if report.HasErrors() {
		os.Exit(1)
	}
}

func printReport(report configcheck.Report) {
	mode := "dev"
	if report.Prod {
		mode = "prod"
	}

	fmt.Printf("DramaBackend 配置检查\n")
	fmt.Printf("mode: %s\n", mode)
	fmt.Printf("issues: %d\n", len(report.Issues))
	if len(report.Issues) == 0 {
		fmt.Println("status: OK")
		return
	}

	status := "OK_WITH_WARNINGS"
	if report.HasErrors() {
		status = "FAILED"
	}
	fmt.Printf("status: %s\n", status)
	for _, issue := range report.Issues {
		fmt.Printf("[%s] %s: %s\n", issue.Severity, issue.Code, issue.Message)
	}
}
