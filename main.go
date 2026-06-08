// planner is a tool for the AI/human plan-review loop: an agent posts a plan,
// a human reviews and comments on it in a browser, the agent reads the comments
// and posts a revised version, and so on. The CLI is the agent's interface; the
// web server (`planner serve`) is the human's. Both share one database.
package main

import (
	"os"

	"planner/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
