package main

import (
	"github.com/dwkaswala/the-guy/internal/agents"
	"github.com/dwkaswala/the-guy/internal/cli"
	"github.com/dwkaswala/the-guy/internal/config"
	"github.com/dwkaswala/the-guy/internal/store"
)

func initState() error {
	if err := config.EnsureDirs(); err != nil {
		return err
	}
	agents.SetBuildVersion(cli.Version)
	return nil
}

func openStore() (*store.Store, error) {
	return store.Open(config.DBPath())
}
