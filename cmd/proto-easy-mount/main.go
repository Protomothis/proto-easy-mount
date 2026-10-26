package main

import (
	"fmt"
	"os"

	"github.com/protomothis/proto-easy-mount/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "오류:", err)
		os.Exit(1)
	}
}
