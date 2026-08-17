package httproute_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"moedex/internal/graph/httproute"
	"moedex/internal/index"
	"moedex/internal/symbol"
)

// The fixture is a miniature service fleet: the server side lives in one shard
// and the clients that call it in another, so every edge the pass finds is one
// no per-repo analysis could have found.

type fixtureFile struct {
	repo    string
	relPath string
	content string
}

var serviceShard = []fixtureFile{
	{"orders-api", "Controllers/OrdersController.cs", `
using Microsoft.AspNetCore.Mvc;

namespace Orders.Api.Controllers;

[ApiController]
[Route("api/[controller]")]
public class OrdersController : ControllerBase
{
    [HttpGet("{id}")]
    public object GetOrder(int id) { return id; }

    [HttpGet]
    public object ListOrders() { return null; }

    [HttpPost]
    public object CreateOrder(object order) { return order; }

    [HttpDelete("{id}")]
    public object DeleteOrder(int id) { return id; }
}
`},
	{"orders-api", "Decoys/Docs.cs", `
public class Docs
{
    // [HttpGet("api/decoy")] and _http.GetAsync("/api/decoy") in a comment.
    private const string Attribute = "[HttpGet(\"api/decoy\")]";
    private const string Call = "_http.GetAsync(\"/api/decoy\")";
    public void Nothing() { }
}
`},
	{"users-api", "Controllers/UsersController.cs", `
using Microsoft.AspNetCore.Mvc;

[ApiController]
[Route("api/[controller]")]
public class UsersController : ControllerBase
{
    [HttpGet("{id}")]
    public object GetUser(int id) { return id; }
}
`},
	{"inventory-svc", "internal/httpapi/routes.go", `package httpapi

import "net/http"

// Register wires the inventory endpoints.
// mux.HandleFunc("/api/decoy", decoy) is commented out and registers nothing.
func Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/inventory/{sku}", getItem)
	mux.HandleFunc("/api/inventory/reports/", reports)
}

func getItem(w http.ResponseWriter, r *http.Request) {}
func reports(w http.ResponseWriter, r *http.Request) {}
`},
	{"bff-node", "server/routes.js", `
const express = require('express');
const app = express();

app.get('/api/cart/:cartId', (req, res) => res.json({}));
app.post('/api/cart', createCart);

// app.get('/api/decoy', decoyHandler);
const docs = "app.get('/api/quoted', h)";

module.exports = app;
`},
	{"reporting-svc", "app.py", `
from flask import Flask

app = Flask(__name__)


@app.route("/api/reports/<int:report_id>")
def get_report(report_id):
    return {}


@app.route("/api/reports", methods=["POST"])
def create_report():
    return {}


@app.get("/api/reports/summary")
def report_summary():
    return {}
`},
}

var clientShard = []fixtureFile{
	{"billing-svc", "Clients/OrderClient.cs", `
using System.Net.Http;

public class OrderClient
{
    private readonly HttpClient _http;
    private readonly string _baseUrl = "";

    public async Task<object> FetchOrder(int id)
    {
        return await _http.GetAsync($"api/orders/{id}");
    }

    public async Task<object> SubmitOrder(object order)
    {
        return await _http.PostAsync("/api/orders", null);
    }

    public async Task<object> FetchViaBaseUrl(int id)
    {
        return await _http.GetAsync($"{_baseUrl}/api/orders/{id}");
    }

    public async Task<object> FetchReport()
    {
        return await _http.GetAsync("/api/reports/17");
    }

    // return await _http.GetAsync("/api/commented");
}
`},
	{"web-ui", "src/api/orders.ts", `
export async function loadOrder(id: string) {
  const res = await fetch(` + "`/api/orders/${id}`" + `);
  return res.json();
}

export async function loadUsers() {
  const res = await fetch('/api/users');
  return res.json();
}

export async function createOrder(body: unknown) {
  return fetch('/api/orders', { method: 'POST', body: JSON.stringify(body) });
}

export async function loadCart(cartId: string) {
  return axios.get(` + "`/api/cart/${cartId}`" + `);
}
`},
	{"inventory-svc", "internal/httpapi/client.go", `package httpapi

import (
	"fmt"
	"io"
	"net/http"
)

func fetchCart(id string) (*http.Response, error) {
	return http.Get(fmt.Sprintf("http://bff/api/cart/%s", id))
}

func createReport(body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest("POST", "http://reporting/api/reports", body)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}
`},
	{"reporting-svc", "clients/orders.py", `
import requests


def fetch_order(order_id):
    return requests.get(f"http://orders-api/api/orders/{order_id}")


def push_metrics(payload):
    return requests.post("http://metrics-svc/api/metrics", json=payload)


def fetch_inventory(sku):
    return requests.request("GET", f"http://inventory/api/inventory/{sku}")
`},
}

func buildShard(t *testing.T, name string, files []fixtureFile) httproute.Shard {
	t.Helper()
	ix := index.New()
	for i, f := range files {
		ix.AddFile(f.repo, f.relPath, "/"+f.repo+"/"+f.relPath, fmt.Sprintf("%s-sha-%d", name, i), []byte(f.content))
	}
	return httproute.Shard{Name: name, Index: ix, Symbols: symbol.BuildMulti(ix)}
}

func fixtureCorpus(t *testing.T) *httproute.Corpus {
	t.Helper()
	return httproute.NewCorpus(
		buildShard(t, "services.shard", serviceShard),
		buildShard(t, "clients.shard", clientShard),
	)
}

func renderEndpoint(e httproute.Endpoint) string {
	return fmt.Sprintf("%s %s %s", e.Repo, e.Method, e.Template)
}

func renderEdge(e httproute.Edge) string {
	return fmt.Sprintf("%s -> %s [%s %s]",
		renderEndpoint(e.Call), renderEndpoint(e.Handler), e.Quality, e.Confidence)
}

// TestExtractFindsBothSidesAcrossLanguages pins the recognizers: ASP.NET
// attribute routing (including the controller-prefix join), Go's ServeMux,
// Express, and Flask on the handler side; HttpClient, fetch/axios, net/http,
// and requests on the client side.
func TestExtractFindsBothSidesAcrossLanguages(t *testing.T) {
	endpoints, report := httproute.Extract(fixtureCorpus(t))

	wantHandlers := []string{
		"bff-node GET /api/cart/{}",
		"bff-node POST /api/cart",
		"inventory-svc ANY /api/inventory/reports/*",
		"inventory-svc GET /api/inventory/{}",
		"orders-api DELETE /api/orders/{}",
		"orders-api GET /api/orders",
		"orders-api GET /api/orders/{}",
		"orders-api POST /api/orders",
		"reporting-svc ANY /api/reports/{}",
		"reporting-svc GET /api/reports/summary",
		"reporting-svc POST /api/reports",
		"users-api GET /api/users/{}",
	}
	wantCalls := []string{
		"billing-svc GET /api/orders/{}",
		"billing-svc GET /api/reports/17",
		"billing-svc GET /{}/api/orders/{}",
		"billing-svc POST /api/orders",
		"inventory-svc GET /api/cart/{}",
		"inventory-svc POST /api/reports",
		"reporting-svc GET /api/inventory/{}",
		"reporting-svc GET /api/orders/{}",
		"reporting-svc POST /api/metrics",
		"web-ui GET /api/cart/{}",
		"web-ui GET /api/orders/{}",
		"web-ui GET /api/users",
		"web-ui POST /api/orders",
	}

	assertSet(t, "handlers", renderAll(endpoints.Handlers), wantHandlers)
	assertSet(t, "calls", renderAll(endpoints.Calls), wantCalls)

	if report.CandidateBlobs == 0 {
		t.Error("CandidateBlobs = 0, want a non-empty trigram fan-out")
	}
	if report.ScannedBlobs == 0 || report.ScannedBlobs > report.CandidateBlobs {
		t.Errorf("ScannedBlobs = %d of %d candidates, want a non-empty subset", report.ScannedBlobs, report.CandidateBlobs)
	}
}

// TestDecoysRegisterNothing proves the two masks carry their weight: an
// attribute or a client call quoted in a string constant, or shown in a
// comment, declares no endpoint.
func TestDecoysRegisterNothing(t *testing.T) {
	endpoints, _ := httproute.Extract(fixtureCorpus(t))
	for _, side := range [][]httproute.Endpoint{endpoints.Handlers, endpoints.Calls} {
		for _, e := range side {
			if strings.Contains(e.Template.String(), "decoy") ||
				strings.Contains(e.Template.String(), "quoted") ||
				strings.Contains(e.Template.String(), "commented") {
				t.Errorf("decoy became a %s endpoint: %s (%s:%s)", e.Role, renderEndpoint(e), e.Repo, e.Path)
			}
		}
	}
}

// TestBuildEmitsCrossServiceEdges is the end-to-end proof. Every edge below
// spans two repositories and was found only because a URL string in one lined
// up with a route template in another.
func TestBuildEmitsCrossServiceEdges(t *testing.T) {
	result := httproute.Build(fixtureCorpus(t))

	want := []string{
		// Structural identity on both sides: Verified.
		"billing-svc GET /api/orders/{} -> orders-api GET /api/orders/{} [Exact Verified]",
		"billing-svc POST /api/orders -> orders-api POST /api/orders [Exact Verified]",
		"web-ui GET /api/orders/{} -> orders-api GET /api/orders/{} [Exact Verified]",
		"web-ui POST /api/orders -> orders-api POST /api/orders [Exact Verified]",
		"web-ui GET /api/cart/{} -> bff-node GET /api/cart/{} [Exact Verified]",
		"inventory-svc GET /api/cart/{} -> bff-node GET /api/cart/{} [Exact Verified]",
		"inventory-svc POST /api/reports -> reporting-svc POST /api/reports [Exact Verified]",
		"reporting-svc GET /api/orders/{} -> orders-api GET /api/orders/{} [Exact Verified]",
		"reporting-svc GET /api/inventory/{} -> inventory-svc GET /api/inventory/{} [Exact Verified]",

		// Binding was required: Pattern.
		"billing-svc GET /{}/api/orders/{} -> orders-api GET /api/orders/{} [Parameterized Pattern]",
		"billing-svc GET /api/reports/17 -> reporting-svc ANY /api/reports/{} [Parameterized Pattern]",
		"reporting-svc GET /api/inventory/{} -> inventory-svc ANY /api/inventory/reports/* [Parameterized Pattern]",
	}

	got := make([]string, 0, len(result.Edges))
	for _, e := range result.Edges {
		got = append(got, renderEdge(e))
		if !e.CrossRepo() {
			t.Errorf("edge is not cross-repo: %s", renderEdge(e))
		}
	}
	assertSet(t, "edges", got, want)

	if result.Report.Edges != len(result.Edges) {
		t.Errorf("Report.Edges = %d, want %d", result.Report.Edges, len(result.Edges))
	}
	if result.Report.Exact != 9 || result.Report.Parameterized != 3 {
		t.Errorf("Exact/Parameterized = %d/%d, want 9/3", result.Report.Exact, result.Report.Parameterized)
	}
	if result.Report.CrossRepo != result.Report.Edges {
		t.Errorf("CrossRepo = %d, want all %d edges", result.Report.CrossRepo, result.Report.Edges)
	}
}

// TestCallsWithNoHandlerStayUnmatched is the negative half of the requirement:
// /api/users must not reach /api/orders, and a call whose server side is
// outside the corpus produces no edge at all rather than a wrong one.
func TestCallsWithNoHandlerStayUnmatched(t *testing.T) {
	result := httproute.Build(fixtureCorpus(t))

	for _, e := range result.Edges {
		if strings.Contains(e.Call.Template.String(), "/api/users") {
			t.Errorf("/api/users matched a handler it must not reach: %s", renderEdge(e))
		}
		if strings.Contains(e.Call.Template.String(), "/api/metrics") {
			t.Errorf("/api/metrics has no handler in the corpus but produced: %s", renderEdge(e))
		}
	}

	// Both unmatched calls are accounted for rather than silently dropped.
	if got := result.Report.UnmatchedCalls(); got != 2 {
		t.Errorf("UnmatchedCalls() = %d, want 2 (/api/users and /api/metrics)", got)
	}
	if result.Report.MatchedCalls+result.Report.UnmatchedCalls() != result.Report.Calls {
		t.Errorf("matched %d + unmatched %d != %d calls",
			result.Report.MatchedCalls, result.Report.UnmatchedCalls(), result.Report.Calls)
	}
}

// TestEdgesCarryTheirEvidence checks that an emitted edge can be traced back to
// the exact bytes that produced it — the URL literal, and the definition it
// sits in — which is what a persisted graph edge needs.
func TestEdgesCarryTheirEvidence(t *testing.T) {
	corpus := fixtureCorpus(t)
	result := httproute.Build(corpus)

	found := false
	for _, e := range result.Edges {
		blob := corpus.Blob(e.Call)
		if blob == nil {
			t.Fatalf("call site of %s resolves to no blob", renderEdge(e))
		}
		if e.Call.Start < 0 || e.Call.End > len(blob.Content) {
			t.Fatalf("call site of %s is out of range", renderEdge(e))
		}
		if got := string(blob.Content[e.Call.Start:e.Call.End]); !strings.Contains(got, e.Call.Raw) {
			t.Errorf("call evidence %q does not contain the parsed URL %q", got, e.Call.Raw)
		}
		if e.Call.Repo == "billing-svc" && e.Call.Symbol == "FetchOrder" {
			found = true
			if e.Handler.Symbol != "GetOrder" {
				t.Errorf("FetchOrder edge points at symbol %q, want GetOrder", e.Handler.Symbol)
			}
		}
	}
	if !found {
		t.Error("no edge was attributed to the enclosing FetchOrder definition")
	}
}

// TestBuildIsDeterministic guards the map iteration inside matching.
func TestBuildIsDeterministic(t *testing.T) {
	first := renderAllEdges(httproute.Build(fixtureCorpus(t)).Edges)
	for i := 0; i < 3; i++ {
		if got := renderAllEdges(httproute.Build(fixtureCorpus(t)).Edges); !equalSlices(got, first) {
			t.Fatalf("run %d differs:\n got %v\nwant %v", i, got, first)
		}
	}
}

func renderAll(eps []httproute.Endpoint) []string {
	out := make([]string, 0, len(eps))
	for _, e := range eps {
		out = append(out, renderEndpoint(e))
	}
	return out
}

func renderAllEdges(edges []httproute.Edge) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, renderEdge(e))
	}
	return out
}

// assertSet compares two multisets of rendered records and reports the
// difference in both directions, so an unexpected extra endpoint is as visible
// as a missing one.
func assertSet(t *testing.T, label string, got, want []string) {
	t.Helper()
	g, w := append([]string(nil), got...), append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	if equalSlices(g, w) {
		return
	}
	t.Errorf("%s: got %d, want %d\nmissing: %v\nextra:   %v",
		label, len(g), len(w), difference(w, g), difference(g, w))
}

func difference(a, b []string) []string {
	counts := map[string]int{}
	for _, s := range b {
		counts[s]++
	}
	var out []string
	for _, s := range a {
		if counts[s] > 0 {
			counts[s]--
			continue
		}
		out = append(out, s)
	}
	return out
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
