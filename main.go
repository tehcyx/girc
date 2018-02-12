package main

import (
	"fmt"
	"net"

	"github.com/tehcyx/girc/server"
)

func main() {

	printBanner()

	fmt.Println("Launching server...")

	// listen on all interfaces
	ln, err := net.Listen("tcp", ":6665")
	if err != nil {
		fmt.Printf("Listen failed, port possibly in use already: %s\n", err)
	}

	defer func() {
		fmt.Printf("Shutting down server. Bye!\n")
		ln.Close()
	}()

	server.InitServer()

	// run loop forever (or until ctrl-c)
	for {
		// accept connection on port
		conn, _ := ln.Accept()

		server.InitClient(conn)
	}
}

func printBanner() {
	// http://patorjk.com/software/taag/#p=display&f=Rectangles&t=girc
	fmt.Println()
	fmt.Printf(`
	Welcome to
                        _         
                    ___|_|___ ___ 
                   | . | |  _|  _|
                   |_  |_|_| |___|
                   |___|          `)
	fmt.Println()
	fmt.Println()
	fmt.Println()
}
