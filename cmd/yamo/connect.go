package main

import (
	"flag"
	"os"

	"github.com/remy/yamo/internal/client"
)

// serverFlags registers the two flags every client command shares.
//
// The catalogue is no longer a client concern: the server owns it, and a
// command line that could open it directly would be a second writer to files
// the server believes it alone touches.
func serverFlags(fs *flag.FlagSet) (server, token *string) {
	server = fs.String("server", os.Getenv("YAMO_SERVER"),
		"yamo server address (default "+client.DefaultServer+", or YAMO_SERVER)")
	token = fs.String("token", os.Getenv("YAMO_TOKEN"),
		"bearer token, if the server requires one (or YAMO_TOKEN)")
	return
}

// connect builds a client from the resolved flags.
func connect(server, token string) (*client.Client, error) {
	return client.FromEnv(server, token)
}
