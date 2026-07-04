// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"context"

	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/github-pr-watcher/pkg/command"
	"github.com/bborbe/github-pr-watcher/pkg/factory"
	libkafkamocks "github.com/bborbe/kafka/mocks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Golden regression test locking down the exact Kafka topic names produced
// by CreateTriggerPRReviewCommandSender (and, by construction, the
// equivalent CreateKafkaSender/CreateCommandConsumer wiring, since all three
// build a cdb.CommandObjectSender from the same TopicPrefix over the same
// lib.GithubPRReviewV1SchemaID{Group: "maintainer", Kind: "githubprreview",
// Version: "v1"}) for the TopicPrefix values that matter in production:
// "develop" (dev stage), "master" (prod stage), and "" (no prefix). The
// literal suffix "request" comes from cdb.SchemaID.CommandTopic →
// cdb.BuildTopic(schemaID, prefix, "request")
// (github.com/bborbe/cqrs@v0.6.0/cdb/cdb_schema-id.go + cdb_build-topic.go).
// If this test's expected literals ever need to change, the topic-prefix
// wiring has regressed — do not "fix" the test, fix the wiring.
var _ = Describe("CreateTriggerPRReviewCommandSender topic naming (golden)", func() {
	sendAndCaptureTopic := func(topicPrefix string) string {
		syncProducer := &libkafkamocks.KafkaSyncProducer{}
		sender := factory.CreateTriggerPRReviewCommandSender(
			context.Background(),
			syncProducer,
			base.TopicPrefix(topicPrefix),
		)
		err := sender.SendCommand(context.Background(), command.TriggerPRReviewCommand{
			URL: "https://github.com/bborbe/repo/pull/1",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(syncProducer.SendMessageCallCount()).To(Equal(1))
		_, msg := syncProducer.SendMessageArgsForCall(0)
		return msg.Topic
	}

	It("prefixes with develop for the dev stage", func() {
		Expect(
			sendAndCaptureTopic("develop"),
		).To(Equal("develop-maintainer-githubprreview-v1-request"))
	})

	It("prefixes with master for the prod stage", func() {
		Expect(
			sendAndCaptureTopic("master"),
		).To(Equal("master-maintainer-githubprreview-v1-request"))
	})

	It("has no prefix when TopicPrefix is empty", func() {
		Expect(sendAndCaptureTopic("")).To(Equal("maintainer-githubprreview-v1-request"))
	})
})
