package config

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/user"

	yaml "gopkg.in/yaml.v3"
)

var Values *Config

type Config struct {
	Server struct {
		Name string `yaml:"name"`
		Host string `yaml:"host"`
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
		ioutil.WriteFile(confPath, []byte("server:\n    host: \"localhost\"\n    port: \"6665\"\n    name: \"daniels server\"\n    motd: \"Find out more on github.com/tehcyx/girc\""), os.ModeAppend)
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
	Values = getConf()
}
