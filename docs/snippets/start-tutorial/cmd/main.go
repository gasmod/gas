// #region main

// Command notesapi serves the notes API built in the tutorial.
package main

import (
	"log"

	"github.com/gasmod/gas/docs/snippets/start-tutorial/app"
)

func main() {
	cfg, err := app.LoadConfig("config.json")
	if err != nil {
		log.Fatal(err)
	}

	if err := app.New(cfg).Run(); err != nil {
		log.Fatal(err)
	}
}

// #endregion main
