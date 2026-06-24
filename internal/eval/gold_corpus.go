package eval

// This file defines a POOLED, MULTI-LANGUAGE real-corpus gold set over four
// repos in ~/TCGitlab, spanning C#, TypeScript, SQL, and ColdFusion. It is the
// trustworthy successor to the single-repo TCSslApiGold() set: every relevance
// label below was produced by POOLING candidate documents from independent
// signals and then judging each candidate by INSPECTING the real file contents.
//
// ---------------------------------------------------------------------------
// REPOS, LANGUAGES, AND SAMPLING (documented so the eval is reproducible)
// ---------------------------------------------------------------------------
//
// The gold set is scored against an index built by goldCorpusRepos() (see
// gold_corpus.go's companion helper in runner_corpus.go). To keep the indexed
// universe modest (the eval must build in seconds, not minutes) we sample by a
// per-repo path predicate rather than ingesting whole repos:
//
//	repo (relative to ~/TCGitlab)                lang   sampled subset
//	-------------------------------------------  -----  ----------------------
//	Services.Registrar/TC.SslApi                 C#     *.cs under src/, excluding
//	                                                     the .Tests project + obj/bin
//	                                                     (mirrors the TS spec/e2e
//	                                                     exclusion; tests are named
//	                                                     after the method under test
//	                                                     and pollute name queries)
//	UIs.Internal/dropcatchadminui                TS     app source *.ts under
//	                                                     src/ (excl. *.spec.ts,
//	                                                     e2e/)
//	Mysql.Utilities/mysql-scripts                SQL    all *.sql (this repo's
//	                                                     .sql files are small,
//	                                                     purpose-named scripts —
//	                                                     unlike the giant schema
//	                                                     dumps in Mysql.Schemas,
//	                                                     which concatenate
//	                                                     hundreds of tables per
//	                                                     file and so cannot be
//	                                                     labeled per-file)
//	coldfusion/hugedomains                       CFML   _inc/*.cfm only — the
//	                                                     include/UDF library of
//	                                                     small action scripts,
//	                                                     each defining or calling
//	                                                     a <cffunction>. The rest
//	                                                     of the ~2200-file app is
//	                                                     page templates, not
//	                                                     labelable units. .cfm
//	                                                     carries symbols
//	                                                     (CFExtractor), so the
//	                                                     symbol arm fires here.
//
// Rationale for these four repos:
//   - TC.SslApi is the one repo with prior hand labels (TCSslApiGold), so the C#
//     arm here EXTENDS that work with pooled, verification-grade labels.
//   - dropcatchadminui is a small Angular admin UI (~28 app .ts files) — small
//     enough to label exhaustively, real enough to exercise the TS symbol arm.
//   - mysql-scripts has 15 small, single-purpose .sql files (replication utils,
//     partition maintenance, history-table generators) — granular enough that a
//     query maps to a specific file, which a schema dump never would.
//   - hugedomains/_inc is the ColdFusion include/UDF library: small action .cfm
//     scripts that define <cffunction> UDFs — granular like mysql-scripts, and
//     the lane that exercises the new CF symbol arm (CFExtractor).
//
// Relevance keys are RelPath WITHIN each repo (ingest sets RelPath relative to
// the repo dir). The four repos have disjoint path shapes (src/TC.SslApi.* vs
// src/app/* vs *.sql / athena/* / replication/* vs _inc/*.cfm), so RelPaths do
// not collide.
//
// ---------------------------------------------------------------------------
// POOLING + VERIFICATION METHOD (per query)
// ---------------------------------------------------------------------------
// For each query, candidate docs were pooled from THREE independent signals:
//   (1) lexical/BM25  — what the moedex ranker itself returns,
//   (2) symbol-name   — files that DEFINE an identifier matching the query,
//   (3) git grep      — a literal/regex pass over the real files.
// Each pooled candidate was then opened and judged by reading the source:
//   grade 2 = the file DEFINES/IMPLEMENTS the concept (a class/proc/func named
//             for it, or the rule/logic that realizes it),
//   grade 1 = the file only MENTIONS/uses the concept.
// Comments cite the real file path + the line/identifier verified. Pooling means
// we labeled files surfaced by signals OTHER than lexical too, which reduces the
// "unlabeled-but-relevant crowds out labeled" bias that TCSslApiGold called out.
//
// TWO-ANNOTATOR ADJUDICATION (2026-06-23). This set was originally single-judge
// (annotator #1). A second annotator (#2) then re-judged all 24 queries BLIND —
// pooling candidates and grading from file contents without seeing #1's labels.
// Inter-annotator agreement on the PRIMARY grade-2 definer was 24/24 (perfect):
// disagreement was confined to grade-1 "mention" labels and a few 2-vs-1 calls.
// A reconciling pass (reading the disputed files directly) resolved every
// disagreement; the labels below are the adjudicated result. Notable changes #2
// forced:
//   - #2 caught two grade-2 definers #1 MISSED: ReissueCertificateValidator.cs
//     ("reissue certificate") and TC.SslJobs/Jobs/SslMonitorJob.cs ("ssl monitor").
//   - #1 caught one #2 wrongly dropped: CsrValidator.cs is literally
//     `class CsrValidator : AbstractValidator<Csr>` ("csr validation rules", =2).
//   - The three prior [NEEDS REVIEW] calls were resolved: ValidationErrorCode.cs
//     DROPPED from "refund order validation" (shared enum, not a definer);
//     util_replication_turbo_button.sql DROPPED from "skip replication error"
//     (#2 confirmed it is replication TUNING, not error-skipping); src/api/api.ts
//     ADDED at grade 1 to "global notification" (both annotators agreed it holds
//     the Create/Update/Remove/Get GlobalNotification contracts).
//   - Vendor/OrderStatus.cs downgraded 2->1 for "order status enum" (it is a
//     request CLASS, not the enum; the enum is SslOrderStatus.cs).
// CATCH-ALL POLICY: aggregator files relevant to many queries (TS
// administration.service.ts, src/api/api.ts; C# SslService.cs; CF
// act_functions*.cfm — 50-100-UDF libraries included on every page) are graded 1
// (not 2) on every query where they contain the specifically-named method — they
// support a feature without being its definer, and grade 1 caps their NDCG
// contribution so a single mega-file cannot dominate the ranking score.
//
// COLDFUSION LANE (2026-06-24, added with the CF symbol arm). The 6 ColdFusion
// queries below are POOLED + READ-VERIFIED single-judge (not the two-annotator
// pass the original 24 went through). Each grade-2 definer was confirmed unique
// across _inc/ (the <cffunction> is defined in exactly one file), and each
// grade-1 caller's call site was read to confirm it invokes that UDF (not a
// same-named method on another object). They primarily exercise the new CF
// symbol arm, whose headline lift is "void transaction" NDCG 0.689->0.964.
//
// SQL LANE (2026-06-24, added with the SQL symbol arm). SQLExtractor now pulls
// top-level CREATE definition names, so all FOUR gold languages exercise the
// symbol arm (the SQL no-op is closed). On THIS gold the SQL arm is neutral, not
// additive: the 6 SQL queries are filename-mirror micro-script lookups already
// won by lexical (see corpusGoldSQL).
//
// PATH ARM + NON-ALIGNED STRATUM (2026-06-24). Ranking includes a filename/path
// RRF arm, ON by default (rank.Config.PathMinCoverage). On the original 30 queries
// (all filename-aligned, because TC names files for their concept) path SUBSUMED
// the symbol arm. Adding corpusGoldNonAligned (6 queries whose definer filename
// does NOT contain the query terms) rebalanced the set and revealed the two arms
// are COMPLEMENTARY: full-stack NDCG ~0.66 -> ~0.93, beating either arm alone
// (~0.84). Path wins the aligned half (and fixed "federated server"/"administration
// service" 0.0 -> 1.0); symbol wins the non-aligned half. The symbol gate was
// raised 0.5 -> 0.67 when the path arm landed. See TestCorpusGoldGate's four-arm
// note for the breakdown.
//
// HARD-DISTRACTOR LABELS (2026-06-24, added with the UDCG metric). A few queries
// carry NEGATIVE grades (grade -1). These are "hard distractors": files the
// adjudication confirmed are plausible-but-WRONG for the query (a same-named symbol
// on a different object, a shared enum, a same-prefix sibling script) AND that the
// lexical/symbol arms actually surface. They are INVISIBLE to nDCG/recall/
// precision/MRR — every one of those guards on grade >= 1, and gain() zeroes
// non-positive grades — so they do NOT move the existing baselines or gate floors.
// They are consumed ONLY by UDCGAtK, which penalizes a distractor that lands in the
// top-k context window (the agent-consumer cost nDCG ignores). The four labeled
// here (void transaction, refund order validation, skip replication error, show
// slave status) reuse files the two-annotator pass already DROPPED, so each is a
// documented, read-verified judgment, not a fresh guess. UDCG is a regression WATCH
// (logged), not a hard gate floor.
//
// CONFIDENCE: every C#/TS/SQL label is grep/read-verified by two independent
// judges and reconciled; the CF and non-aligned labels are single-judge
// pooled+read-verified. This set GATES (see TestCorpusGoldGate): the lexical-only
// baseline and the full production stack (lexical+path+symbol) must clear
// regression thresholds set below the measured baseline. Absolute numbers still
// come from a small (~36-query) pooled set, so treat them as a regression watch
// with a defensible floor, not a published claim.

// CorpusGold returns the pooled multi-language gold set. ~36 queries:
// ~10 C#, ~8 TypeScript, ~6 SQL, ~6 ColdFusion, + 6 NON-FILENAME-ALIGNED
// (corpusGoldNonAligned) that deliberately sample the region the original 30
// under-represent (see that function's header).
func CorpusGold() []GoldQuery {
	var gold []GoldQuery
	gold = append(gold, corpusGoldCSharp()...)
	gold = append(gold, corpusGoldTypeScript()...)
	gold = append(gold, corpusGoldSQL()...)
	gold = append(gold, corpusGoldColdFusion()...)
	gold = append(gold, corpusGoldNonAligned()...)
	return gold
}

// corpusGoldCSharp: TC.SslApi (C#). Paths verified to exist + contents read.
func corpusGoldCSharp() []GoldQuery {
	return []GoldQuery{
		// "generate csr": CsrHelper.GenerateCsr (CsrHelper.cs:19) is THE
		// implementation; GenerateCsr DTO (CsrDtos.cs:7) and GenerateCsrValidator
		// (:6) define the request + its rules. Pool also surfaced SslService.cs and
		// SubmitOrder.cs which only call/reference CSR -> grade 1.
		{Query: "generate csr", Relevant: map[string]int{
			"src/TC.SslApi.Service/CsrHelper.cs":                       2, // GenerateCsr() impl
			"src/TC.SslApi.Models/CsrDtos.cs":                          2, // GenerateCsr/Csr DTOs
			"src/TC.SslApi.Service/Validation/GenerateCsrValidator.cs": 1, // rules for the request
		}},
		// "csr validation rules": the two FluentValidation validators that DEFINE
		// CSR rules. CsrValidator.cs:6 (class CsrValidator : AbstractValidator<Csr>)
		// and GenerateCsrValidator.cs:6. [Annotator #2 dropped CsrValidator; #1
		// confirmed by reading it — it is the canonical CSR field-rule validator,
		// kept at 2.] Pool surfaced ValidationTests/SslService (mention only).
		{Query: "csr validation rules", Relevant: map[string]int{
			"src/TC.SslApi.Service/Validation/CsrValidator.cs":         2,
			"src/TC.SslApi.Service/Validation/GenerateCsrValidator.cs": 2,
		}},
		// "refund order validation": RefundOrderValidator.cs:9 defines the eligible-
		// statuses + accept-date rule (the single grade-2 definer). HARD DISTRACTOR
		// (grade -1, UDCG only): ValidationErrorCode.cs is a shared, generic error-code
		// enum [ADJUDICATED: annotator #2 judged it NOT a definer of refund validation]
		// that the lexical arm surfaces for any "...validation" query —
		// plausible-but-wrong, penalized rather than silently dropped.
		{Query: "refund order validation", Relevant: map[string]int{
			"src/TC.SslApi.Service/Validation/RefundOrderValidator.cs": 2,
			"src/TC.SslApi.Service/Models/ValidationErrorCode.cs":      -1,
		}},
		// "ssl contact validation": SslContactValidator.cs:6 defines first/last/
		// email/phone NotEmpty rules. High confidence.
		{Query: "ssl contact validation", Relevant: map[string]int{
			"src/TC.SslApi.Service/Validation/SslContactValidator.cs": 2,
		}},
		// "change approver method": ChangeApproverMethod DTO (Dtos/) + the vendor
		// ChangeApprover (Vendor/). Mirrors TCSslApiGold "change approver" but
		// pooled here against the larger src/ tree. ResendApproverEmail mentions
		// approver -> grade 1.
		// [#2 added SslService.cs at grade 1 — it implements the ChangeApproverMethod
		// service call (catch-all policy: grade 1).]
		{Query: "change approver method", Relevant: map[string]int{
			"src/TC.SslApi.Models/Dtos/ChangeApproverMethod.cs":  2,
			"src/TC.SslApi.Models/Vendor/ChangeApprover.cs":      2,
			"src/TC.SslApi.Models/Vendor/ResendApproverEmail.cs": 1,
			"src/TC.SslApi.Service/SslService.cs":                1,
		}},
		// "fetch approver list": FetchApproverList DTO + vendor ApproverList /
		// GetApproverList. High confidence on the DTO (named for the concept).
		// [#2 added SslService.cs (implements FetchApproverList) at grade 1.]
		{Query: "fetch approver list", Relevant: map[string]int{
			"src/TC.SslApi.Models/Dtos/FetchApproverList.cs":                      2,
			"src/TC.SslApi.Models/Vendor/ApproverList.cs":                         1,
			"src/TC.SslApi.Service/Vendors/TheSslStore/Models/GetApproverList.cs": 1,
			"src/TC.SslApi.Service/SslService.cs":                                 1,
		}},
		// "reissue certificate": ReIssueCertificate DTO + its validator + Vendor model.
		// [#2 caught ReissueCertificateValidator.cs (class ReissueCertificateValidator
		// : AbstractValidator<ReIssueCertificate>), a grade-2 definer #1 MISSED; and
		// SslService.cs (implements ReissueCertificate) at grade 1.]
		// NOTE: still a known tokenizer asymmetry (query "reissue" vs identifier
		// "ReIssue"/"Reissue"); kept to keep that finding measurable.
		{Query: "reissue certificate", Relevant: map[string]int{
			"src/TC.SslApi.Models/Dtos/ReIssueCertificate.cs":                 2,
			"src/TC.SslApi.Service/Validation/ReissueCertificateValidator.cs": 2,
			"src/TC.SslApi.Models/Vendor/ReIssueCertificate.cs":               1,
			"src/TC.SslApi.Service/SslService.cs":                             1,
		}},
		// "download certificate": both the DTO and the vendor request DEFINE it.
		// [#2 added SslService.cs (implements DownloadCertificate) at grade 1.]
		{Query: "download certificate", Relevant: map[string]int{
			"src/TC.SslApi.Models/Dtos/DownloadCertificate.cs":   2,
			"src/TC.SslApi.Models/Vendor/DownloadCertificate.cs": 2,
			"src/TC.SslApi.Service/SslService.cs":                1,
		}},
		// "ssl monitor": SslMonitor.cs is the model; SslMonitorJob.cs is the job that
		// runs monitoring (CheckAllSslMonitors). [#2 caught SslMonitorJob.cs, a
		// grade-2 definer #1 MISSED.] The Get/Upsert DTOs + failed-test event mention
		// it -> grade 1.
		{Query: "ssl monitor", Relevant: map[string]int{
			"src/TC.SslApi.Models/SslMonitor.cs":                  2,
			"src/TC.SslJobs/Jobs/SslMonitorJob.cs":                2,
			"src/TC.SslApi.Models/Dtos/GetSslMonitor.cs":          1,
			"src/TC.SslApi.Models/Dtos/UpsertSslMonitor.cs":       1,
			"src/TC.SslApi.Models/Events/SslMonitorTestFailed.cs": 1,
		}},
		// "order status enum": SslOrderStatus.cs IS the enum (grade 2). [ADJUDICATED:
		// Vendor/OrderStatus.cs downgraded 2->1 — it is a request CLASS named
		// OrderStatus, not the enum the query asks for.] CheckOrderStatus DTO uses it.
		{Query: "order status enum", Relevant: map[string]int{
			"src/TC.SslApi.Models/SslOrderStatus.cs":        2,
			"src/TC.SslApi.Models/Vendor/OrderStatus.cs":    1,
			"src/TC.SslApi.Models/Dtos/CheckOrderStatus.cs": 1,
		}},
	}
}

// corpusGoldTypeScript: dropcatchadminui (Angular TS). Contents read; .ts files
// carry symbols (TSExtractor) so the symbol arm is exercised here.
func corpusGoldTypeScript() []GoldQuery {
	return []GoldQuery{
		// "send bulk email": BulkEmailComponent.sendEmail() (bulk-email.component.ts:20)
		// is the UI handler (grade 2 — the feature's definer). [CATCH-ALL POLICY:
		// administration.service.ts downgraded 2->1 — it is a shared ~20-method
		// service that wraps the call, not the feature definer.] api.ts holds the
		// SendBulkEmail contract -> grade 1.
		{Query: "send bulk email", Relevant: map[string]int{
			"src/app/bulk-email/bulk-email.component.ts": 2,
			"src/app/services/administration.service.ts": 1,
			"src/api/api.ts": 1, // SendBulkEmail among many contracts
		}},
		// "create auctions": CreateAuctionsComponent.createAuctions()
		// (create-auctions.component.ts:19) is the definer (grade 2). [CATCH-ALL:
		// administration.service.ts -> 1.] api.ts has the CreateAuctions contract.
		{Query: "create auctions", Relevant: map[string]int{
			"src/app/create-auctions/create-auctions.component.ts": 2,
			"src/app/services/administration.service.ts":           1,
			"src/api/api.ts": 1,
		}},
		// "global notification": GlobalNotificationsComponent
		// (global-notifications.component.ts) is the UI; the model
		// (models/global-notification.ts) defines IGlobalNotification +
		// GlobalNotificationType. [ADJUDICATED: api.ts ADDED at grade 1 — both
		// annotators confirmed it holds Create/Update/Remove/Get GlobalNotification
		// contracts. administration.service.ts kept at 1 (has the CRUD methods).]
		{Query: "global notification", Relevant: map[string]int{
			"src/app/global-notifications/global-notifications.component.ts": 2,
			"src/app/models/global-notification.ts":                          2,
			"src/app/services/administration.service.ts":                     1,
			"src/api/api.ts": 1,
		}},
		// "permissions service": permissions.service.ts:9 defines PermissionsService
		// with hasPermission(). The single definer. High confidence.
		{Query: "permissions service", Relevant: map[string]int{
			"src/app/services/permissions.service.ts": 2,
		}},
		// "has permission check": same file — hasPermission() at
		// permissions.service.ts:22 is the method (the definer of that method ->
		// grade 2; #2 graded 1, but this file is the sole definer, not a catch-all).
		{Query: "has permission", Relevant: map[string]int{
			"src/app/services/permissions.service.ts": 2,
		}},
		// "route guard can activate": AdminStagingGuard implements CanActivate
		// (admin-staging.guard.ts:7), canActivate() at :9. High confidence; the only
		// guard in the repo.
		{Query: "route guard can activate", Relevant: map[string]int{
			"src/app/admin-staging.guard.ts": 2,
		}},
		// "catch backorders": CatchBackordersComponent
		// (catch-backorders.component.ts:11) loads/flags backorders;
		// AdministrationService.loadBackorders/flagBackorder (administration.service.ts).
		// api.ts has LoadBackorders/FlagBackorder contracts -> grade 1. [CATCH-ALL:
		// administration.service.ts downgraded 2->1.]
		{Query: "catch backorders", Relevant: map[string]int{
			"src/app/catch-backorders/catch-backorders.component.ts": 2,
			"src/app/services/administration.service.ts":             1,
			"src/api/api.ts": 1,
		}},
		// "administration service http": administration.service.ts:19 defines
		// AdministrationService, which wraps HttpService for all admin calls. Single
		// definer. High confidence.
		{Query: "administration service", Relevant: map[string]int{
			"src/app/services/administration.service.ts": 2,
		}},
	}
}

// corpusGoldSQL: mysql-scripts. .sql files DO carry symbols now (SQLExtractor
// pulls top-level CREATE PROCEDURE/FUNCTION/TABLE/VIEW/TRIGGER/EVENT/DATABASE
// names), so the symbol arm fires here. But it is MEASURABLY NEUTRAL on this gold:
// these are micro-scripts whose proc name == its filename and appears in the
// (tiny) body, so BM25 already ranks the definer #1 (5/6 queries are NDCG 1.0
// lexically) and the symbol vote is redundant. The lane's value is coverage +
// symbol-boundary context for the contextwin/MCP consumer, not a ranking lift
// here — unlike ColdFusion, where "void transaction" had a symbol that beat
// lexical. Contents read per file.
func corpusGoldSQL() []GoldQuery {
	return []GoldQuery{
		// "partition maintenance procedure": partition_maintenance.sql defines
		// PROCEDURE date_partition_maintenance (line 2). Single definer. High conf.
		{Query: "partition maintenance procedure", Relevant: map[string]int{
			"partition_maintenance.sql": 2,
		}},
		// "skip replication error": util_replication_skip_multisource.sql defines a
		// proc that sets SQL_SLAVE_SKIP_COUNTER=1 (the literal "skip a replication
		// error" operation) — the single grade-2 definer. HARD DISTRACTOR (grade -1,
		// UDCG only): util_replication_turbo_button.sql [ADJUDICATED: annotator #2 read
		// it and confirmed it only toggles innodb_flush_log_at_trx_commit/sync_binlog
		// for catch-up speed — replication TUNING, not error-skipping] is a same-prefix
		// replication util the lexical arm surfaces, plausible-but-wrong.
		{Query: "skip replication error", Relevant: map[string]int{
			"replication/util_replication_skip_multisource.sql": 2,
			"replication/util_replication_turbo_button.sql":     -1,
		}},
		// "show slave status": util_replication_show_slave_status.sql (named for it,
		// runs SHOW SLAVE STATUS) — the single grade-2 definer. HARD DISTRACTOR (grade
		// -1, UDCG only): util_help_replication.sql [ADJUDICATED: a help/index proc that
		// lists ALL replication utils, a low-information catch-all, not a definer]
		// mentions slave status and is surfaced for this query — plausible-but-wrong.
		{Query: "show slave status", Relevant: map[string]int{
			"replication/util_replication_show_slave_status.sql": 2,
			"replication/util_help_replication.sql":              -1,
		}},
		// "create history table trigger": create_history_scripts.sql defines
		// util_create_history_scripts, which generates CREATE TABLE *_history +
		// CREATE TRIGGER trg_*_history_insert. Single definer. High confidence.
		{Query: "create history table trigger", Relevant: map[string]int{
			"history_tables/create_history_scripts.sql": 2,
		}},
		// "federated server": mysql_create_federated_server.sql is the only file
		// that emits CREATE SERVER ... FOREIGN DATA WRAPPER 'mysql'. The word
		// "federated" appears ONLY in the filename, never in the body (a SELECT
		// CONCAT('CREATE SERVER ''',..) that BUILDS the DDL dynamically), so the
		// content-only lexical/symbol arms score it 0.0. The filename/path arm
		// (on by default) is exactly what surfaces it: NDCG 0.0 -> 1.0. This was the
		// motivating case for the path arm.
		{Query: "federated server", Relevant: map[string]int{
			"mysql_create_federated_server.sql": 2,
		}},
		// "heartbeat table": heartbeat.sql creates database+table mysql_heartbeat
		// (lines 2,29). Single definer. High confidence.
		{Query: "heartbeat table", Relevant: map[string]int{
			"heartbeat.sql": 2,
		}},
	}
}

// corpusGoldColdFusion: hugedomains/_inc (ColdFusion .cfm includes). The CF
// symbol arm (CFExtractor) pulls <cffunction> names, so these queries exercise
// it: each grade-2 definer is the single _inc file that DEFINES a <cffunction>
// matching the query (verified unique across _inc — the function is defined in
// exactly one file), and grade-1 mentions are files that CALL it, confirmed by
// reading the call site (not just a name grep). RelPaths are repo-relative
// (_inc/...), disjoint from the C#/TS/SQL path shapes so keys never collide.
func corpusGoldColdFusion() []GoldQuery {
	return []GoldQuery{
		// "void transaction": act_voidTransaction.cfm:5 defines
		// <cffunction name="voidTransaction"> (PayPal void). act_auctionCancelOldBids
		// cfincludes it and calls voidTransaction() (:23) -> grade 1. HARD DISTRACTOR
		// (grade -1, UDCG only): act_recurring-paypal-to-ppv4.cfm contains a
		// voidTransaction that is a gateway-object METHOD
		// (rInit.transaction().voidTransaction) — a different symbol the lexical/symbol
		// arms surface for this query, plausible-but-wrong.
		{Query: "void transaction", Relevant: map[string]int{
			"_inc/act_voidTransaction.cfm":          2,
			"_inc/act_auctionCancelOldBids.cfm":     1,
			"_inc/act_recurring-paypal-to-ppv4.cfm": -1,
		}},
		// "unzip file": act_fun_zip.cfm defines gUnZip (:28) and gUnzipFile (:52).
		// dsp_cachePre.cfm calls gUnZip() to decompress the page cache -> grade 1.
		{Query: "unzip file", Relevant: map[string]int{
			"_inc/act_fun_zip.cfm":  2,
			"_inc/dsp_cachePre.cfm": 1,
		}},
		// "gzip compress": act_fun_zip.cfm:17 defines gZip. dsp_cachePost.cfm and
		// dsp_cachePre.cfm call gZip() to compress page output -> grade 1.
		{Query: "gzip compress", Relevant: map[string]int{
			"_inc/act_fun_zip.cfm":   2,
			"_inc/dsp_cachePost.cfm": 1,
			"_inc/dsp_cachePre.cfm":  1,
		}},
		// "which payment plan version": act_paymentPlanSwitcher.cfm:15 defines
		// <cffunction name="whichPaymentPlanVersion">. Single definer in _inc.
		{Query: "which payment plan version", Relevant: map[string]int{
			"_inc/act_paymentPlanSwitcher.cfm": 2,
		}},
		// "which shopping cart version": same file, :130 defines
		// whichShoppingCartVersion — a distinct concept/symbol, same definer file.
		{Query: "which shopping cart version", Relevant: map[string]int{
			"_inc/act_paymentPlanSwitcher.cfm": 2,
		}},
		// "buying guide link": qry_buyingGuideAll.cfm defines buyingGuideNextLinkFunc
		// (:46) and buyingGuidePreviousLinkFunc (:23), the prev/next nav-link UDFs.
		// Single definer in _inc.
		{Query: "buying guide link", Relevant: map[string]int{
			"_inc/qry_buyingGuideAll.cfm": 2,
		}},
	}
}

// corpusGoldNonAligned is a NON-FILENAME-ALIGNED stratum (2026-06-24), added to
// correct a sampling bias the original 30 queries exposed once the path arm
// landed: TC names files for their concept, so those queries were dominated by the
// filename/path arm and the symbol/content arms were under-rewarded (see
// TestCorpusGoldGate's four-arm note — path SUBSUMED symbol there). These queries
// deliberately target concepts whose DEFINER FILENAME does NOT contain the query
// terms, so the path arm cannot trivially win and the symbol/content (and, under
// -tags onnx, dense) arms have to do the work. They measure the half of the search
// space the original gold misses.
//
// The richest honest source is generically-named libraries that DEFINE many
// concepts: ColdFusion's _inc/act_functions*.cfm (8000+-line UDF libraries whose
// name says nothing about any one function) and C# SslService.cs (a service whose
// method names express features no eponymous file carries). SQL/TS are absent here:
// SQL files ARE their proc name (filename-aligned by construction) and the small
// Angular app names every file for its feature.
//
// Each grade-2 definer was READ and verified: (1) it is the file that actually
// DEFINES the named function/method (a <cffunction name="X"> tag, or a C# method
// body — not a mention); (2) for CF, the name is defined in EXACTLY ONE _inc file
// (grep-confirmed unique); (3) the FILENAME contains none of the query terms
// (that is the whole point); (4) the query terms cover the definer's identifier
// tokens enough to be answerable. Callers live in page templates / controllers
// outside the indexed sample, so most are grade-2-only (like the existing CF/SQL
// single-definer queries). RelPaths are repo-relative; CF (_inc/*.cfm) and C#
// (src/*.cs) shapes are disjoint from each other and the rest of the gold.
func corpusGoldNonAligned() []GoldQuery {
	return []GoldQuery{
		// "elasticsearch related domains": act_functions.cfm:3910 defines
		// <cffunction name="ElasticSearchRelatedDomains"> (doc comment: "Search ...
		// ElasticSearch data for related domain names"). The filename act_functions.cfm
		// says nothing about elasticsearch/related/domains; the UDF name does.
		{Query: "elasticsearch related domains", Relevant: map[string]int{
			"_inc/act_functions.cfm": 2,
		}},
		// "parse user agent": act_functions3.cfm:4309 defines
		// <cffunction name="parseUserAgent"> (returns a struct of mobile/family/bot).
		{Query: "parse user agent", Relevant: map[string]int{
			"_inc/act_functions3.cfm": 2,
		}},
		// "minfraud api factors": act_functions3.cfm:6473 defines
		// <cffunction name="minFraudApiFactors"> (MaxMind minFraud scoring; queries the
		// minFraudFactors table). Concept lives only in the UDF name.
		{Query: "minfraud api factors", Relevant: map[string]int{
			"_inc/act_functions3.cfm": 2,
		}},
		// "escape for json": act_functions3.cfm:7246 defines
		// <cffunction name="escapeForJson"> (escapes quotes/slashes for JSON safety).
		{Query: "escape for json", Relevant: map[string]int{
			"_inc/act_functions3.cfm": 2,
		}},
		// "meta refresh": act_functions3.cfm:2440 defines
		// <cffunction name="metaRefresh"> (emits an HTML <meta http-equiv="refresh">
		// redirect). Filename act_functions3.cfm carries neither "meta" nor "refresh".
		{Query: "meta refresh", Relevant: map[string]int{
			"_inc/act_functions3.cfm": 2,
		}},
		// "parse vendor error messages": SslService.cs:639 defines
		// ParseVendorErrorMessages (splits vendor "ErrorMessage:..." strings) — the
		// grade-2 implementation; the file is the generic SslService aggregate, not
		// named for the concept. Pooling (the symbol arm surfaced these) found the
		// supporting cast, all read-verified -> grade 1: ISslService.cs:59 DECLARES the
		// contract; SslResponseBase.cs defines the ErrorMessage model; and
		// SslStoreResponseMaps.cs:129 (GetErrorMessages) regex-parses TheSslStore's
		// vendor error strings into ErrorMessages (a vendor-specific handler of the
		// same concept). The .Tests project is no longer indexed (see goldCorpusRepos),
		// which removes the test-name distractor.
		{Query: "parse vendor error messages", Relevant: map[string]int{
			"src/TC.SslApi.Service/SslService.cs":                                    2, // ParseVendorErrorMessages impl
			"src/TC.SslApi.Service/ISslService.cs":                                   1, // declares the contract
			"src/TC.SslApi.Models/Vendor/SslResponseBase.cs":                         1, // ErrorMessage model
			"src/TC.SslApi.Service/Vendors/TheSslStore/Maps/SslStoreResponseMaps.cs": 1, // vendor-specific parser
		}},
	}
}
