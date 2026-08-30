package main

import (
	"fmt"
	"os"

	"github.com/yasyf/cc-runtime/runtime"
)

func main() {
	if err := runtime.Root().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
