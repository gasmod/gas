package main

import (
	"log"

	"github.com/gasmod/gas/example/templates-basic/app"
)

func main() {
	if err := app.New().Run(); err != nil {
		log.Fatal(err)
	}
}
