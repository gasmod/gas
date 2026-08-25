// File Vault API — a complete Gas example demonstrating DI, routing, auth,
// database, migrations, caching, file storage, job queues, email, and
// structured logging working together.
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
