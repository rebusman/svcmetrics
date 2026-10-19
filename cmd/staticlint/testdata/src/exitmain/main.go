package main

import (
	"fmt"
	. "os"
	stdos "os"
)

func main() {
	defer fmt.Println("never printed")

	stdos.Exit(1)   // want "direct call to os.Exit in main function of package main"
	Exit(2)         // want "direct call to os.Exit in main function of package main"
	(stdos.Exit)(3) // want "direct call to os.Exit in main function of package main"

	go func() {
		stdos.Exit(4) // want "direct call to os.Exit in main function of package main"
	}()

	exit := stdos.Exit
	exit(5)
}

func run() {
	stdos.Exit(6)
}

type server struct{}

func (server) main() {
	stdos.Exit(7)
}
