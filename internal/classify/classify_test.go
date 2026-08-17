package classify_test

import (
	"fmt"
	"sort"
	"testing"

	"moedex/internal/classify"
	"moedex/internal/index"
	"moedex/internal/symbol"
)

var csharpFixture = map[string]string{
	"Controllers/OrdersController.cs": `
using Microsoft.AspNetCore.Mvc;

[ApiController]
[Route("api/[controller]")]
public class OrdersController
{
    [HttpGet("{id}")]
    public object GetOrder(int id) { return id; }

    [HttpPost]
    public object CreateOrder(object order) { return order; }

    [HttpPut("{id}")]
    public object UpdateOrder(int id, object order) { return order; }

    [HttpDelete("{id}")]
    public object DeleteOrder(int id) { return id; }
}

public class UnmarkedController
{
    public void Work() { }
}
`,
	"Events/OrderSubmitted.cs": `
using MassTransit;

public record OrderSubmitted(int OrderId);

public class OrderSubmittedConsumer : IConsumer<OrderSubmitted>
{
    public object Consume(object context) { return context; }
}
`,
	"Events/ConventionOnly.cs": `
public record InventoryAdjusted(int ProductId);
public class InventoryAdjustedConsumer { }
`,
	"Data/AppDbContext.cs": `
using Microsoft.EntityFrameworkCore;

public class Customer { }

public class AppDbContext
{
    public DbSet<Customer> Customers { get; set; }
}

public class FactoryUser
{
    private readonly IDbContextFactory<AppDbContext> _factory;
}
`,
	"Services/OrderService.cs": `
public interface IOrderService { }
public class OrderService : IOrderService { }
public class TransientWorker { }
public class SingletonClock { }
`,
	"Services/Startup.cs": `
public class Startup
{
    public void ConfigureServices(IServiceCollection services)
    {
        services.AddScoped<IOrderService, OrderService>();
        services.AddTransient<TransientWorker>();
        services.AddSingleton<object, SingletonClock>();
    }
}
`,
	"Messaging/Publishers.cs": `
using MassTransit;

public class BusPublisher
{
    private readonly IPublishEndpoint _endpoint;
    public object SendViaEndpoint(object message) { return message; }
}

public class DirectBusPublisher
{
    private readonly IBus _bus;
    public object PublishDirect(object message) { return _bus.Publish(message); }
}

public class StaticBusPublisher
{
    public object PublishStatic(object message) { return IBus.Publish(message); }
}
`,
	"Decoys/Plain.cs": `
public class Plain
{
    private const string RouteText = "[HttpDelete]";
    // services.AddSingleton<IPlain, Plain>();
    // private IPublishEndpoint endpoint;
    public void Ordinary() { }
}
`,
	"Decoys/NotCSharp.go": `package decoys

// [ApiController] IConsumer<Fake> DbSet<Fake> AddTransient<Fake> IPublishEndpoint
type Fake struct{}
`,
}

func fixture(t *testing.T) (*index.Index, *symbol.Index) {
	t.Helper()
	ix := index.New()
	paths := make([]string, 0, len(csharpFixture))
	for path := range csharpFixture {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for i, path := range paths {
		ix.AddFile("fixture", path, "/fixture/"+path, fmt.Sprintf("sha-%d", i), []byte(csharpFixture[path]))
	}
	return ix, symbol.BuildMulti(ix)
}

func TestFrameworkClassifiersFindFixtureInstances(t *testing.T) {
	tests := []struct {
		name       string
		classify   func(*index.Index, *symbol.Index) classify.Report
		wantKind   symbol.Kind
		wantNames  []string
		stillPlain []string
	}{
		{
			name:       "routes",
			classify:   classify.ClassifyRoutes,
			wantKind:   symbol.Route,
			wantNames:  []string{"CreateOrder", "DeleteOrder", "GetOrder", "OrdersController", "UpdateOrder"},
			stillPlain: []string{"UnmarkedController", "Work", "Plain", "Ordinary"},
		},
		{
			name:       "events",
			classify:   classify.ClassifyEvents,
			wantKind:   symbol.Event,
			wantNames:  []string{"InventoryAdjusted", "OrderSubmitted"},
			stillPlain: []string{"OrderSubmittedConsumer", "Consume", "Plain"},
		},
		{
			name:       "tables",
			classify:   classify.ClassifyTables,
			wantKind:   symbol.Table,
			wantNames:  []string{"AppDbContext", "Customer"},
			stillPlain: []string{"FactoryUser", "Plain"},
		},
		{
			name:       "services",
			classify:   classify.ClassifyServices,
			wantKind:   symbol.Service,
			wantNames:  []string{"OrderService", "SingletonClock", "TransientWorker"},
			stillPlain: []string{"IOrderService", "Startup", "ConfigureServices", "Plain"},
		},
		{
			name:       "queues",
			classify:   classify.ClassifyQueues,
			wantKind:   symbol.Queue,
			wantNames:  []string{"BusPublisher", "PublishDirect", "PublishStatic"},
			stillPlain: []string{"DirectBusPublisher", "StaticBusPublisher", "Plain", "Ordinary"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ix, sx := fixture(t)
			report := tt.classify(ix, sx)
			if report.Kind != tt.wantKind {
				t.Fatalf("report kind = %s, want %s", report.Kind, tt.wantKind)
			}
			if report.CandidateBlobs == 0 || report.CandidateBlobs >= ix.NumBlobs() {
				t.Fatalf("CandidateBlobs = %d of %d, want a non-empty trigram-filtered subset", report.CandidateBlobs, ix.NumBlobs())
			}
			if got := matchNames(report); !equalStrings(got, tt.wantNames) {
				t.Fatalf("matches = %v, want %v", got, tt.wantNames)
			}
			if report.Promoted() != len(tt.wantNames) {
				t.Errorf("Promoted() = %d, want %d", report.Promoted(), len(tt.wantNames))
			}
			for _, name := range tt.wantNames {
				if got := kindsOf(sx, name); !containsKind(got, tt.wantKind) {
					t.Errorf("%s kinds = %v, want one %s", name, got, tt.wantKind)
				}
			}
			for _, name := range tt.stillPlain {
				for _, got := range kindsOf(sx, name) {
					if got == tt.wantKind {
						t.Errorf("decoy %s was incorrectly classified %s", name, tt.wantKind)
					}
				}
			}

			// A second pass confirms the same evidence but performs no new
			// promotions, proving classification is stable across rebuild steps.
			again := tt.classify(ix, sx)
			if got := matchNames(again); !equalStrings(got, tt.wantNames) {
				t.Fatalf("idempotent matches = %v, want %v", got, tt.wantNames)
			}
			if again.Promoted() != 0 {
				t.Errorf("second Promoted() = %d, want 0", again.Promoted())
			}
		})
	}
}

func TestClassifyAllPromotesEveryArchitecturalKind(t *testing.T) {
	ix, sx := fixture(t)
	summary := classify.ClassifyAll(ix, sx)

	for _, tc := range []struct {
		report classify.Report
		kind   symbol.Kind
	}{
		{summary.Routes, symbol.Route},
		{summary.Events, symbol.Event},
		{summary.Tables, symbol.Table},
		{summary.Queues, symbol.Queue},
		{summary.Services, symbol.Service},
	} {
		if tc.report.Kind != tc.kind || len(tc.report.Matches) == 0 {
			t.Errorf("%s report = kind %s, matches %v", tc.kind, tc.report.Kind, matchNames(tc.report))
		}
	}

	want := map[string]symbol.Kind{
		"OrdersController": symbol.Route,
		"GetOrder":         symbol.Route,
		"OrderSubmitted":   symbol.Event,
		"Customer":         symbol.Table,
		"AppDbContext":     symbol.Table,
		"BusPublisher":     symbol.Queue,
		"PublishDirect":    symbol.Queue,
		"PublishStatic":    symbol.Queue,
		"OrderService":     symbol.Service,
	}
	for name, kind := range want {
		if got := kindsOf(sx, name); !containsKind(got, kind) {
			t.Errorf("%s kinds = %v, want one %s", name, got, kind)
		}
	}
}

func TestClassifiersHandleNilIndexes(t *testing.T) {
	for _, fn := range []func(*index.Index, *symbol.Index) classify.Report{
		classify.ClassifyRoutes,
		classify.ClassifyEvents,
		classify.ClassifyTables,
		classify.ClassifyQueues,
		classify.ClassifyServices,
	} {
		if got := fn(nil, nil); got.CandidateBlobs != 0 || len(got.Matches) != 0 {
			t.Errorf("nil classifier result = %+v, want empty", got)
		}
	}
}

func matchNames(report classify.Report) []string {
	out := make([]string, len(report.Matches))
	for i, m := range report.Matches {
		out[i] = m.Name
	}
	sort.Strings(out)
	return out
}

func kindsOf(sx *symbol.Index, name string) []symbol.Kind {
	var out []symbol.Kind
	for _, ref := range sx.Definitions(name) {
		for _, s := range sx.Symbols(ref.Blob) {
			if s.NameStart == ref.Start {
				out = append(out, s.Kind)
			}
		}
	}
	return out
}

func containsKind(kinds []symbol.Kind, want symbol.Kind) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa, bb := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
