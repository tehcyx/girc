package main

import (
	"fmt"
	"net"

	log "github.com/sirupsen/logrus"

	"github.com/tehcyx/girc/internal/config"
	"github.com/tehcyx/girc/pkg/server"
)

func main() {
	log.Println("Launching server...")

	// listen on all interfaces
	ln, err := net.Listen("tcp", fmt.Sprintf(":%s", config.Values.Server.Port))
	if err != nil {
		log.Error(fmt.Errorf("listen failed, port possibly in use already: %w", err))
	}

	defer func() {
		log.Printf("Shutting down server. Bye!\n")
		ln.Close()
	}()

	ircSrv := server.New()

	// run loop forever (or until ctrl-c)
	for {
		// accept connection on port
		conn, _ := ln.Accept()

		ircSrv.HandleClient(conn)
	}
}
