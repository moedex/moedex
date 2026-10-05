package eval

import "moedex/internal/ingest"

// This fixture is a deliberately MULTI-LANGUAGE corpus — Go, C#, SQL,
// TypeScript, YAML — mirroring configured's actual polyglot stack rather than
// a C#-only monoculture. It is hermetic (in-memory, no external corpus) so the
// ranking measurement runs anywhere and is reproducible.
//
// It is also designed to exercise the Go-only symbol arm: the "refund" concept
// appears across languages, but only Go DEFINES a symbol named Refund, so the
// symbol-name ranking arm should surface the Go definer. The with/without-symbol
// comparison in the measurement test quantifies that.

func file(rel, content string) ingest.File {
	return ingest.File{
		Repo:    "fixture",
		RelPath: rel,
		AbsPath: "/fixture/" + rel,
		SHA:     "sha-" + rel,
		Content: []byte(content),
	}
}

// FixtureFiles returns the multi-language corpus.
func FixtureFiles() []ingest.File {
	return []ingest.File{
		// --- Go (parseable → carries symbols) ---
		file("billing/refund.go", `package billing

// Refund reverses a captured charge for the given order.
func Refund(orderID string) error {
	return nil
}
`),
		file("billing/charge.go", `package billing

// Charge represents a captured payment.
type Charge struct {
	OrderID string
}

// CaptureCharge captures a pending charge.
func CaptureCharge(c Charge) error {
	return nil
}
`),
		file("search/index.go", `package search

// BuildIndex builds the trigram index over a set of documents.
func BuildIndex(docs []string) {
}
`),
		// Go auth file. Note it never uses the words "login"/"credentials":
		// it speaks of "authenticate"/"session". A query phrased with the
		// user-facing synonym ("login credentials") shares NO trigram with
		// this file's identifiers, so pure lexical cannot find it — the dense
		// concept arm can (auth axis). This is the dense-favoring case.
		file("auth/authenticate.go", `package auth

// Authenticate verifies a user's identity and opens a session.
func Authenticate(user, secret string) (token string, err error) {
	return "", nil
}
`),
		// --- C# (no Go symbols → lexical only) ---
		file("Models/RefundOrder.cs", `namespace Example.Billing.Models
{
    // Request to refund an existing order.
    public class RefundOrder
    {
        public string OrderId { get; set; }
    }
}
`),
		file("Services/SslOrderService.cs", `namespace Example.Ssl
{
    public class SslOrderService
    {
        // Issue an SSL certificate for an approved order.
        public void IssueCertificate() { }
    }
}
`),
		// --- SQL (MySQL/MariaDB flavor) ---
		file("db/refund_table.sql", `-- refund audit log
CREATE TABLE refund_log (
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  order_id BIGINT NOT NULL,
  amount DECIMAL(10,2)
) ENGINE=InnoDB;
`),
		file("db/orders.sql", `CREATE TABLE orders (
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  status VARCHAR(32)
) ENGINE=InnoDB;
`),
		// --- TypeScript (Angular) ---
		file("web/refund.component.ts", `import { Component } from '@angular/core';

@Component({ selector: 'app-refund' })
export class RefundComponent {
  refund(orderId: string): void {}
}
`),
		file("web/order.service.ts", `import { Injectable } from '@angular/core';

@Injectable()
export class OrderService {
  getOrder(id: string) {}
}
`),
		// --- YAML (GitLab CI) ---
		file("ci/.gitlab-ci.yml", `stages:
  - build
  - test
  - deploy

build_job:
  stage: build
  script:
    - dotnet build
`),
	}
}

// FixtureGold is the hand-authored relevance set over FixtureFiles. Grades:
// 2 = primary (implements/defines the concept), 1 = related mention. Relevance
// is a genuine judgment — code that implements "refund" is more relevant than a
// log table or UI shell — which is exactly the signal the symbol/dense arms
// should help surface.
//
// The set is grouped by the search INTENT it exercises so the labels stay
// honest and auditable. Each query's relevance is justified by what literally
// appears in FixtureFiles (above): a file is graded 2 only when it defines or
// implements the concept, 1 when it merely mentions a term. Where a query is
// designed to favor the dense arm, the comment says so and explains why lexical
// cannot reach the target.
func FixtureGold() []GoldQuery {
	return []GoldQuery{
		// --- Multi-term, cross-language concept queries ---
		// "refund order" appears as terms across 4 files; the two that NAME the
		// concept (Go func Refund, C# class RefundOrder) are primary.
		{Query: "refund order", Relevant: map[string]int{
			"billing/refund.go":       2,
			"Models/RefundOrder.cs":   2,
			"web/refund.component.ts": 1,
			"db/refund_table.sql":     1,
		}},
		// Single bare term: every refund-y file mentions it, but the Go file
		// DEFINES a symbol named Refund — the symbol arm's target.
		{Query: "refund", Relevant: map[string]int{
			"billing/refund.go":       2, // the Go definer (symbol arm target)
			"Models/RefundOrder.cs":   1,
			"web/refund.component.ts": 1,
			"db/refund_table.sql":     1,
		}},

		// --- Identifier / symbol-name intent (symbol arm should help) ---
		// "capture charge" is the CaptureCharge func name split; charge.go is the
		// only definer.
		{Query: "capture charge", Relevant: map[string]int{"billing/charge.go": 2}},
		// CamelCase identifier typed as one token. tokenindex splits CaptureCharge
		// -> capture+charge, so this should still resolve to the same definer.
		{Query: "CaptureCharge", Relevant: map[string]int{"billing/charge.go": 2}},
		// Class name as a query: only the C# file declares class RefundOrder.
		{Query: "RefundOrder class", Relevant: map[string]int{"Models/RefundOrder.cs": 2}},
		// "build index" names func BuildIndex in search/index.go uniquely; the CI
		// YAML's "build" stage is an unrelated homonym and must NOT be graded.
		{Query: "build index trigram", Relevant: map[string]int{"search/index.go": 2}},

		// --- Domain phrase queries (lexical baseline) ---
		{Query: "ssl certificate issue", Relevant: map[string]int{"Services/SslOrderService.cs": 2}},
		// "issue certificate" is the IssueCertificate method; same primary file.
		{Query: "issue certificate", Relevant: map[string]int{"Services/SslOrderService.cs": 2}},
		{Query: "order service", Relevant: map[string]int{"web/order.service.ts": 2}},
		{Query: "create table orders", Relevant: map[string]int{"db/orders.sql": 2}},
		// SQL audit table: refund_log is in refund_table.sql only.
		{Query: "refund log table", Relevant: map[string]int{"db/refund_table.sql": 2}},
		{Query: "build deploy stage", Relevant: map[string]int{"ci/.gitlab-ci.yml": 2}},
		// Angular component shell.
		{Query: "refund component selector", Relevant: map[string]int{"web/refund.component.ts": 2}},

		// --- Synonym / concept queries (DENSE arm should help) ---
		// "reverse a payment" is a synonym for refund. billing/refund.go's doc
		// comment literally says "reverses a captured charge", so it shares the
		// term "reverses"; but the C# RefundOrder uses none of these words, so it
		// is graded 1. The dense concept arm bridges reverse<->refund/charge.
		{Query: "reverse a payment", Relevant: map[string]int{
			"billing/refund.go":     2,
			"billing/charge.go":     1,
			"Models/RefundOrder.cs": 1,
		}},
		// Pure dense-favoring case: auth/authenticate.go never contains the words
		// "login" or "credentials" — it says "Authenticate"/"identity"/"session".
		// Lexical retrieval shares NO trigram with the query and should miss it;
		// the dense concept arm (auth axis) is the only path to the right file.
		{Query: "login credentials", Relevant: map[string]int{"auth/authenticate.go": 2}},
		// "user identity session" overlaps the doc comment lexically AND maps to
		// the auth concept — both arms should agree here (sanity anchor for the
		// synonym group).
		{Query: "user identity session", Relevant: map[string]int{"auth/authenticate.go": 2}},
	}
}
