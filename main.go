package main

import (
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/user"

	log "github.com/sirupsen/logrus"

	"github.com/tehcyx/girc/server"
	yaml "gopkg.in/yaml.v3"
)

var conf *Config

type Config struct {
	Server struct {
		Name string `yaml:"name"`
		Port string `yaml:"port"`
		Motd string `yaml:"motd"`
	}
}

func getConf() *Config {
	c := &Config{}
	osUser, err := user.Current()
	if err != nil {
		panic(err)
	}
	userHome := osUser.HomeDir

	configDir := fmt.Sprintf("%s%s.girc%s", userHome, string(os.PathSeparator), string(os.PathSeparator))
	if _, err := os.Stat(configDir); os.IsNotExist(err) {
		os.Mkdir(configDir, os.ModeDir)
	}
	confPath := fmt.Sprintf("%sconf.yaml", configDir)
	if _, err := os.Stat(confPath); os.IsNotExist(err) {
		ioutil.WriteFile(confPath, []byte("server:\n    port: \"6665\"\n    name: \"daniels server\"\n    motd: \"Find out more on github.com/tehcyx/girc\""), os.ModeAppend)
	}

	yamlFile, err := ioutil.ReadFile(confPath)
	if err != nil {
		log.Printf("yamlFile.Get err   #%v ", err)
	}
	err = yaml.Unmarshal(yamlFile, c)
	if err != nil {
		log.Fatalf("Unmarshal: %v", err)
	}
	return c
}

func init() {
	conf = getConf()
}

func main() {
	log.Println("Launching server...")

	// listen on all interfaces
	ln, err := net.Listen("tcp", fmt.Sprintf(":%s", conf.Server.Port))
	if err != nil {
		log.Error(fmt.Errorf("listen failed, port possibly in use already: %w", err))
	}

	defer func() {
		log.Printf("Shutting down server. Bye!\n")
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
