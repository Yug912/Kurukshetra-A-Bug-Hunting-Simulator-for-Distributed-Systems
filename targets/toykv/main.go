// Command toykv is a small Raft key-value store (hashicorp/raft) that follows
// docs/PROTOCOL.md. It is the default target Kurukshetra hunts bugs in; the
// BUG_* env flags turn on deliberate consistency bugs.
package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	cfg, err := ConfigFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	n, err := Start(cfg)
	if err != nil {
		log.Fatal(err)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	n.Shutdown()
}
