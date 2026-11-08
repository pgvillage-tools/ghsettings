// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

package config_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestConfig(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "config suite")
}
