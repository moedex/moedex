package eval

import "moedex/internal/ingest"

// This fixture is a deliberately MULTI-LANGUAGE corpus — Go, C#, SQL,
// TypeScript, YAML — mirroring TurnCommerce's actual polyglot stack rather than
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
		// --- C# (no Go symbols → lexical only) ---
		file("Models/RefundOrder.cs", `namespace TC.Billing.Models
{
    // Request to refund an existing order.
    public class RefundOrder
    {
        public string OrderId { get; set; }
    }
}
`),
		file("Services/SslOrderService.cs", `namespace TC.Ssl
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
// log table or UI shell — which is exactly the signal the symbol arm should help
// surface.
func FixtureGold() []GoldQuery {
	return []GoldQuery{
		{Query: "refund order", Relevant: map[string]int{
			"billing/refund.go":       2,
			"Models/RefundOrder.cs":   2,
			"web/refund.component.ts": 1,
			"db/refund_table.sql":     1,
		}},
		{Query: "refund", Relevant: map[string]int{
			"billing/refund.go":       2, // the Go definer (symbol arm target)
			"Models/RefundOrder.cs":   1,
			"web/refund.component.ts": 1,
			"db/refund_table.sql":     1,
		}},
		{Query: "capture charge", Relevant: map[string]int{"billing/charge.go": 2}},
		{Query: "build index trigram", Relevant: map[string]int{"search/index.go": 2}},
		{Query: "ssl certificate issue", Relevant: map[string]int{"Services/SslOrderService.cs": 2}},
		{Query: "order service", Relevant: map[string]int{"web/order.service.ts": 2}},
		{Query: "create table orders", Relevant: map[string]int{"db/orders.sql": 2}},
		{Query: "build deploy stage", Relevant: map[string]int{"ci/.gitlab-ci.yml": 2}},
	}
}
