package main

import (
	"fmt"
	"os"
)

func main() {
	hostname, err := os.ReadFile("/etc/hostname")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("guest pid=%d read %d hostname bytes\n", os.Getpid(), len(hostname))
}
