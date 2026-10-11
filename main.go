package main

import (
	"fmt"
	"os"
)

func main() {
	app, err := newApp(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	if err == nil {
		err = app.run()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "services:", err)
		os.Exit(1)
	}
}
