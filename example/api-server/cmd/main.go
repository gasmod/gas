package main

import (
	"log"

	"github.com/gasmod/gas/example/api-server/app"
)

func main() {
	if err := app.New().Run(); err != nil {
		log.Fatal(err)
	}
}
