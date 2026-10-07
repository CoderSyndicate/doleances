// Command frontend is one of the three doléances services.
//
// This file wires and nothing else: the service itself lives in
// internal/frontend. Anything worth a unit test belongs there, not here.
package main

import (
	"os"

	"github.com/CoderSyndicate/doleances/internal/cli"
	"github.com/CoderSyndicate/doleances/internal/frontend"
)

// Set at build time:
//
//	-ldflags "-X main.version=x.y.z -X main.commit=abc1234"
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	os.Exit(cli.Execute(frontend.Definition(), version, commit))
}
