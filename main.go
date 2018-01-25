package main

import "net"
import "fmt"
import "bufio"
import "strings" // only needed below for sample processing

func main() {

	printBanner()

	fmt.Println("Launching server...")

	// listen on all interfaces
	ln, _ := net.Listen("tcp", ":6665")

	// accept connection on port
	conn, _ := ln.Accept()

	// run loop forever (or until ctrl-c)
	for {
		// will listen for message to process ending in newline (\n)
		message, _ := bufio.NewReader(conn).ReadString('\n')
		// output message received
		fmt.Print("Message Received:", string(message))
		// sample process for string received
		newmessage := strings.ToUpper(message)
		// send new string back to client
		conn.Write([]byte(newmessage + "\n"))
	}
}

func printBanner() {
	// http://patorjk.com/software/taag/#p=display&f=Rectangles&t=gomanager
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
