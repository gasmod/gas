package main

import (
	"log"

	"github.com/gasmod/gas/example/hello-world-2/app"
)

func main() {
	if err := app.New().Run(); err != nil {
		log.Fatal(err)
	}
}
