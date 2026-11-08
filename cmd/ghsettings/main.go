// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

// Package main is the ghsettings command: it manages the settings of GitHub repositories from a declarative config.
package main

import (
	"errors"
	"fmt"
	"os"
)

// version is set at build time
var version = "No version defined at build time"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		var exit exitError
		if errors.As(err, &exit) {
			os.Exit(int(exit))
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

// exitError ends the command with an exit code, without an error message.
type exitError int

func (e exitError) Error() string {
	return fmt.Sprintf("exit code %d", int(e))
}
