// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

package workflows_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestWorkflows(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "workflows suite")
}
