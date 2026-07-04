// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"

	taskmocks "github.com/bborbe/agent/mocks"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/github-pr-watcher/mocks"
	"github.com/bborbe/github-pr-watcher/pkg"
	"github.com/bborbe/github-pr-watcher/pkg/factory"
	"github.com/bborbe/github-pr-watcher/pkg/filter"
	libkafkamocks "github.com/bborbe/kafka/mocks"
	kvmocks "github.com/bborbe/kv/mocks"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CreateTriggerPRReviewCommandSender", func() {
	It("returns a non-nil sender", func() {
		syncProducer := new(libkafkamocks.KafkaSyncProducer)
		sender := factory.CreateTriggerPRReviewCommandSender(
			context.Background(),
			syncProducer,
			base.TopicPrefix("develop"),
		)
		Expect(sender).NotTo(BeNil())
	})
})

var _ = Describe("CreateCommandConsumer", func() {
	It("returns a non-nil run.Func when all dependencies are non-nil", func() {
		syncProducer := new(libkafkamocks.KafkaSyncProducer)
		saramaClientProvider := new(libkafkamocks.KafkaSaramaClientProvider)
		db := new(kvmocks.DB)
		ghClient := new(mocks.GitHubClient)
		createSender := new(taskmocks.TaskCreateCommandSender)
		taskCreationFilter := filter.TaskCreationFilters{}
		trustDecision := new(mocks.Trust)

		runFunc := factory.CreateCommandConsumer(
			saramaClientProvider,
			syncProducer,
			db,
			ghClient,
			createSender,
			taskCreationFilter,
			trustDecision,
			"dev", 80, 200, "",
			pkg.NewMetrics(),
			base.TopicPrefix("develop"),
			libtime.NewCurrentDateTime(),
		)
		Expect(runFunc).NotTo(BeNil())
	})

	It("CreateCommandConsumer body has no control flow", func() {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "factory.go", nil, parser.AllErrors)
		Expect(err).NotTo(HaveOccurred())
		var fn *ast.FuncDecl
		for _, decl := range file.Decls {
			if f, ok := decl.(*ast.FuncDecl); ok && f.Name.Name == "CreateCommandConsumer" {
				fn = f
				break
			}
		}
		Expect(fn).NotTo(BeNil(), "CreateCommandConsumer not found")
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch n.(type) {
			case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt:
				Fail(fmt.Sprintf(
					"CreateCommandConsumer body contains forbidden control flow: %T at %v",
					n, fset.Position(n.Pos()),
				))
			}
			return true
		})
	})
})

var _ = Describe("NewMemDB", func() {
	It("returns a non-nil DB", func() {
		db := pkg.NewMemDB()
		Expect(db).NotTo(BeNil())
	})

	It("implements the libkv.DB interface (Sync, Close, Remove, Stats are callable)", func() {
		db := pkg.NewMemDB()
		Expect(db.Sync()).To(Succeed())
		Expect(db.Close()).To(Succeed())
		Expect(db.Remove()).To(Succeed())
		stats, err := db.Stats(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(stats).NotTo(BeNil())
	})
})
