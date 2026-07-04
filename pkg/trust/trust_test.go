// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package trust_test

import (
	"context"
	"fmt"

	"github.com/bborbe/errors"
	"github.com/bborbe/github-pr-watcher/pkg/trust"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// alwaysTrust returns a Trust that always succeeds with the given label.
func alwaysTrust(label string) trust.Trust {
	return trust.Func(func(_ context.Context, _ trust.PR) (trust.Result, error) {
		return trust.NewResult(true, label), nil
	})
}

// alwaysDeny returns a Trust that always denies with the given label.
func alwaysDeny(label string) trust.Trust {
	return trust.Func(func(_ context.Context, _ trust.PR) (trust.Result, error) {
		return trust.NewResult(false, label), nil
	})
}

// alwaysError returns a Trust that always returns an error.
func alwaysError(msg string) trust.Trust {
	return trust.Func(func(ctx context.Context, _ trust.PR) (trust.Result, error) {
		return nil, errors.Errorf(ctx, "%s", msg)
	})
}

var _ = Describe("trust.And", func() {
	pr := trust.PR{AuthorLogin: "alice"}

	It("succeeds when all members trust", func() {
		a := trust.And{alwaysTrust("leaf-a"), alwaysTrust("leaf-b")}
		r, err := a.IsTrusted(context.Background(), pr)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeTrue())
		Expect(r.Description()).To(ContainSubstring("leaf-a"))
		Expect(r.Description()).To(ContainSubstring("leaf-b"))
	})

	It("denies when any member denies, collecting all descriptions", func() {
		a := trust.And{alwaysTrust("leaf-a"), alwaysDeny("leaf-b")}
		r, err := a.IsTrusted(context.Background(), pr)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeFalse())
		Expect(r.Description()).To(ContainSubstring("leaf-a"))
		Expect(r.Description()).To(ContainSubstring("leaf-b"))
	})

	It("evaluates all members even after first denial (full audit trail)", func() {
		callCount := 0
		counter := trust.Func(func(_ context.Context, _ trust.PR) (trust.Result, error) {
			callCount++
			return trust.NewResult(false, fmt.Sprintf("leaf-%d", callCount)), nil
		})
		a := trust.And{alwaysDeny("first"), counter, counter}
		r, err := a.IsTrusted(context.Background(), pr)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeFalse())
		Expect(callCount).To(Equal(2), "And must evaluate all members for complete audit trail")
	})

	It("wraps errors from members", func() {
		a := trust.And{alwaysTrust("ok"), alwaysError("boom")}
		_, err := a.IsTrusted(context.Background(), pr)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("boom"))
	})

	It("empty And returns vacuous success", func() {
		a := trust.And{}
		r, err := a.IsTrusted(context.Background(), pr)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeTrue())
	})
})

var _ = Describe("trust.Or", func() {
	pr := trust.PR{AuthorLogin: "alice"}

	It("succeeds when any member trusts", func() {
		o := trust.Or{alwaysDeny("leaf-a"), alwaysTrust("leaf-b")}
		r, err := o.IsTrusted(context.Background(), pr)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeTrue())
		Expect(r.Description()).To(ContainSubstring("leaf-a"))
		Expect(r.Description()).To(ContainSubstring("leaf-b"))
	})

	It("denies when all members deny", func() {
		o := trust.Or{alwaysDeny("leaf-a"), alwaysDeny("leaf-b")}
		r, err := o.IsTrusted(context.Background(), pr)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeFalse())
	})

	It("evaluates all members (full audit trail)", func() {
		callCount := 0
		counter := trust.Func(func(_ context.Context, _ trust.PR) (trust.Result, error) {
			callCount++
			return trust.NewResult(true, fmt.Sprintf("leaf-%d", callCount)), nil
		})
		o := trust.Or{alwaysTrust("first"), counter, counter}
		_, err := o.IsTrusted(context.Background(), pr)
		Expect(err).NotTo(HaveOccurred())
		Expect(callCount).To(Equal(2), "Or must evaluate all members for complete audit trail")
	})

	It("wraps errors from members", func() {
		o := trust.Or{alwaysError("boom"), alwaysTrust("ok")}
		_, err := o.IsTrusted(context.Background(), pr)
		Expect(err).To(HaveOccurred())
	})

	It("empty Or returns vacuous failure", func() {
		o := trust.Or{}
		r, err := o.IsTrusted(context.Background(), pr)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeFalse())
	})
})

var _ = Describe("trust.Not", func() {
	pr := trust.PR{AuthorLogin: "alice"}

	It("inverts a trusting leaf", func() {
		r, err := trust.Not(alwaysTrust("leaf")).IsTrusted(context.Background(), pr)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeFalse())
		Expect(r.Description()).To(ContainSubstring("not("))
		Expect(r.Description()).To(ContainSubstring("leaf"))
	})

	It("inverts a denying leaf", func() {
		r, err := trust.Not(alwaysDeny("leaf")).IsTrusted(context.Background(), pr)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeTrue())
	})

	It("propagates errors from the wrapped leaf", func() {
		_, err := trust.Not(alwaysError("boom")).IsTrusted(context.Background(), pr)
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("nested compositions", func() {
	pr := trust.PR{AuthorLogin: "alice"}

	It("And{Or{trusted,denied}, Not{denied}} succeeds", func() {
		compound := trust.And{
			trust.Or{alwaysTrust("or-a"), alwaysDeny("or-b")},
			trust.Not(alwaysDeny("not-leaf")),
		}
		r, err := compound.IsTrusted(context.Background(), pr)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeTrue())
		// Audit trail contains leaf labels from all levels
		Expect(r.Description()).To(ContainSubstring("or-a"))
		Expect(r.Description()).To(ContainSubstring("not-leaf"))
	})

	It("And{Or{denied,denied}, Not{trusted}} denies with full trail", func() {
		compound := trust.And{
			trust.Or{alwaysDeny("or-a"), alwaysDeny("or-b")},
			trust.Not(alwaysTrust("not-leaf")),
		}
		r, err := compound.IsTrusted(context.Background(), pr)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeFalse())
		Expect(r.Description()).To(ContainSubstring("or-a"))
		Expect(r.Description()).To(ContainSubstring("or-b"))
		Expect(r.Description()).To(ContainSubstring("not-leaf"))
	})
})
