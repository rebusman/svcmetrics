package main

import "github.com/rebusman/svcmetrics/internal/agent"

func main() {
	agent.New("http://localhost:8080", 2, 10).Run()
}
