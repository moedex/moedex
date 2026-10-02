package semantic

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// DomainFact is a versioned interpretation of a bound framework API in a
// captured compiler context. It does not establish runtime activation, message
// delivery, or database contents. The containing binding supplies API identity,
// source occurrence, enclosing symbol, and compiler provenance.
type DomainFact struct {
	Kind          string         `json:"kind"`
	Rule          string         `json:"rule"`
	EvidenceScope string         `json:"evidence_scope"`
	Targets       []DomainTarget `json:"targets"`
	Lifetime      string         `json:"lifetime,omitempty"`
	Table         string         `json:"table,omitempty"`
	Schema        string         `json:"schema,omitempty"`
	RoutePattern  string         `json:"route_pattern,omitempty"`
}

type DomainTarget struct {
	Role     string `json:"role"`
	SymbolID string `json:"symbol_id"`
}

const MaxDomainFacts = 8

// ValidateDomainFacts validates the bounded wire shape; artifact/index readers
// must additionally validate target references and the bound framework API.
func ValidateDomainFacts(facts []DomainFact) error {
	if len(facts) > MaxDomainFacts {
		return fmt.Errorf("semantic: too many domain facts")
	}
	seen := map[string]bool{}
	for _, f := range facts {
		newKind := f.Kind == "storage_entity_use" || f.Kind == "storage_entity_mapping"
		expectedRule := "csharp-framework-v1"
		if newKind {
			expectedRule = "csharp-framework-v2"
		}
		if f.Kind == "storage_context_registration" {
			expectedRule = "csharp-framework-v3"
		}
		if f.Kind == "message_publish_configuration" || f.Kind == "message_event_configuration" {
			expectedRule = "csharp-framework-v4"
		}
		if f.Kind == "di_registration_if_absent" || f.Kind == "message_response" {
			expectedRule = "csharp-framework-v5"
		}
		if f.Kind == "di_keyed_registration" || f.Kind == "di_keyed_registration_configuration" {
			expectedRule = "csharp-keyed-di-v1"
		}
		if f.Kind == "message_request_configuration" {
			expectedRule = "csharp-framework-v6"
		}
		if f.Kind == "endpoint_post_configuration" {
			expectedRule = "csharp-endpoint-v1"
		}
		if f.Kind == "mediator_send_configuration" {
			expectedRule = "csharp-mediatr-v1"
		}
		if f.Kind == "di_open_generic_registration_configuration" {
			expectedRule = "csharp-open-di-v1"
		}
		if f.Kind == "mediator_assembly_scan_configuration" {
			expectedRule = "csharp-mediatr-scan-v1"
		}
		if f.Kind == "consumer_namespace_scan_configuration" || f.Kind == "activity_namespace_scan_configuration" {
			expectedRule = "csharp-masstransit-scan-v1"
		}
		if f.Kind == "routing_slip_activity_configuration" {
			expectedRule = "csharp-routing-slip-v1"
		}
		if f.Rule != expectedRule || f.EvidenceScope != "compile_time" {
			return fmt.Errorf("semantic: unsupported domain evidence")
		}
		for _, s := range []string{f.Lifetime, f.Table, f.Schema, f.RoutePattern} {
			if len(s) > 1024 || !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
				return fmt.Errorf("semantic: invalid domain value")
			}
		}
		var roles []string
		switch f.Kind {
		case "di_keyed_registration", "di_keyed_registration_configuration":
			roles = []string{"service", "implementation", "key_type", "registration_api"}
			if !oneOf(f.Lifetime, "singleton", "scoped", "transient") {
				return fmt.Errorf("semantic: invalid keyed lifetime")
			}
		case "di_open_generic_registration_configuration":
			roles = []string{"service_template", "implementation_template"}
			if !oneOf(f.Lifetime, "singleton", "scoped", "transient") {
				return fmt.Errorf("semantic: invalid open DI lifetime")
			}
		case "di_registration", "di_registration_if_absent", "storage_context_registration":
			roles = []string{"service", "implementation"}
			if !oneOf(f.Lifetime, "singleton", "scoped", "transient") {
				return fmt.Errorf("semantic: invalid DI lifetime")
			}
		case "message_response", "message_publish", "message_consumer", "message_publish_configuration", "message_event_configuration":
			roles = []string{"message"}
		case "consumer_namespace_scan_configuration", "activity_namespace_scan_configuration":
			roles = []string{"namespace_marker"}
		case "routing_slip_activity_configuration":
			roles = []string{"activity", "arguments", "address_field", "formatter_api"}
		case "mediator_assembly_scan_configuration":
			roles = []string{"assembly_marker"}
		case "mediator_send_configuration":
			roles = []string{"request", "response"}
		case "endpoint_post_configuration":
			roles = []string{"handler"}
		case "message_request_configuration":
			roles = []string{"request", "response_1", "response_2"}
		case "storage_entity", "storage_table", "storage_entity_use", "storage_entity_mapping":
			roles = []string{"entity"}
		default:
			return fmt.Errorf("semantic: unsupported domain kind")
		}
		if (f.Kind != "di_open_generic_registration_configuration" && f.Kind != "di_registration" && f.Kind != "di_registration_if_absent" && f.Kind != "storage_context_registration" && f.Kind != "di_keyed_registration" && f.Kind != "di_keyed_registration_configuration" && f.Lifetime != "") || (f.Kind != "storage_table" && (f.Table != "" || f.Schema != "")) || (f.Kind == "storage_table" && strings.TrimSpace(f.Table) == "") {
			return fmt.Errorf("semantic: inconsistent domain attributes")
		}
		if f.Kind == "endpoint_post_configuration" && strings.TrimSpace(f.RoutePattern) == "" || f.Kind != "endpoint_post_configuration" && f.RoutePattern != "" {
			return fmt.Errorf("semantic: inconsistent endpoint pattern")
		}
		if len(f.Targets) != len(roles) {
			return fmt.Errorf("semantic: invalid domain target count")
		}
		for i, t := range f.Targets {
			if t.Role != roles[i] || !strings.HasPrefix(t.SymbolID, "symbol:") || len(t.SymbolID) != 71 {
				return fmt.Errorf("semantic: invalid domain target")
			}
		}
		if f.Kind == "storage_context_registration" && f.Targets[0].SymbolID != f.Targets[1].SymbolID {
			return fmt.Errorf("semantic: single-type context registration requires identical targets")
		}
		b, _ := json.Marshal(f)
		if seen[string(b)] {
			return fmt.Errorf("semantic: duplicate domain fact")
		}
		seen[string(b)] = true
	}
	return nil
}

// ValidateDomainAPI rejects project-local lookalikes and facts attached to a
// different framework method. Assembly identity is captured evidence, not an
// assertion of package publisher authenticity.
func ValidateDomainAPI(f DomainFact, api Symbol) error {
	k := api.Key
	if f.Kind == "di_keyed_registration_configuration" {
		if k.Language == "csharp" && k.NamespaceKind == "project" && k.DescriptorKind == "documentation_comment_id" && strings.HasPrefix(k.Descriptor, "M:") && strings.Contains(k.Descriptor, "``") {
			return nil
		}
		return fmt.Errorf("semantic: keyed source configuration requires source generic helper")
	}
	if k.Language != "csharp" || k.NamespaceKind != "assembly" || k.DescriptorKind != "documentation_comment_id" {
		return fmt.Errorf("semantic: domain API must be a metadata C# symbol")
	}
	assembly := strings.SplitN(k.Namespace, ",", 2)[0]
	d := k.Descriptor
	valid := false
	switch f.Kind {
	case "di_keyed_registration":
		valid = validKeyedAPI(api, f.Lifetime) && len(f.Targets) == 4 && f.Targets[3].SymbolID == api.ID
	case "di_open_generic_registration_configuration":
		method := map[string]string{"singleton": "AddSingleton", "scoped": "AddScoped", "transient": "AddTransient"}[f.Lifetime]
		valid = assembly == "Microsoft.Extensions.DependencyInjection.Abstractions" && method != "" && d == "M:Microsoft.Extensions.DependencyInjection.ServiceCollectionServiceExtensions."+method+"(Microsoft.Extensions.DependencyInjection.IServiceCollection,System.Type,System.Type)"
	case "di_registration":
		method := map[string]string{"singleton": "AddSingleton", "scoped": "AddScoped", "transient": "AddTransient"}[f.Lifetime]
		prefix := "M:Microsoft.Extensions.DependencyInjection.ServiceCollectionServiceExtensions." + method
		valid = assembly == "Microsoft.Extensions.DependencyInjection.Abstractions" && (d == prefix+"``1(Microsoft.Extensions.DependencyInjection.IServiceCollection)" || d == prefix+"``2(Microsoft.Extensions.DependencyInjection.IServiceCollection)")
	case "di_registration_if_absent":
		method := map[string]string{"singleton": "TryAddSingleton", "scoped": "TryAddScoped", "transient": "TryAddTransient"}[f.Lifetime]
		prefix := "M:Microsoft.Extensions.DependencyInjection.Extensions.ServiceCollectionDescriptorExtensions." + method
		valid = assembly == "Microsoft.Extensions.DependencyInjection.Abstractions" && (d == prefix+"``1(Microsoft.Extensions.DependencyInjection.IServiceCollection)" || d == prefix+"``2(Microsoft.Extensions.DependencyInjection.IServiceCollection)")
		if valid && strings.Contains(d, "``1(") {
			valid = len(f.Targets) == 2 && f.Targets[0].SymbolID == f.Targets[1].SymbolID
		}
	case "consumer_namespace_scan_configuration", "activity_namespace_scan_configuration":
		method := "AddConsumersFromNamespaceContaining"
		if f.Kind == "activity_namespace_scan_configuration" {
			method = "AddActivitiesFromNamespaceContaining"
		}
		valid = assembly == "MassTransit" && d == "M:MassTransit.RegistrationExtensions."+method+"``1(MassTransit.IRegistrationConfigurator,System.Func{System.Type,System.Boolean})"
	case "routing_slip_activity_configuration":
		valid = assembly == "MassTransit.Abstractions" && d == "M:MassTransit.IItineraryBuilder.AddActivity(System.String,System.Uri,System.Object)"
	case "mediator_assembly_scan_configuration":
		valid = assembly == "MediatR" && d == "M:Microsoft.Extensions.DependencyInjection.MediatRServiceConfiguration.RegisterServicesFromAssembly(System.Reflection.Assembly)"
	case "mediator_send_configuration":
		valid = assembly == "MediatR" && d == "M:MediatR.ISender.Send``1(MediatR.IRequest{``0},System.Threading.CancellationToken)"
	case "endpoint_post_configuration":
		valid = assembly == "Microsoft.AspNetCore.Routing" && d == "M:Microsoft.AspNetCore.Builder.EndpointRouteBuilderExtensions.MapPost(Microsoft.AspNetCore.Routing.IEndpointRouteBuilder,System.String,System.Delegate)"
	case "message_request_configuration":
		valid = assembly == "MassTransit.Abstractions" && (d == "M:MassTransit.IRequestClient`1.GetResponse``2(`0,System.Threading.CancellationToken,MassTransit.RequestTimeout)" || d == "M:MassTransit.IRequestClient`1.GetResponse``2(System.Object,System.Threading.CancellationToken,MassTransit.RequestTimeout)")
	case "message_response":
		valid = assembly == "MassTransit.Abstractions" && (d == "M:MassTransit.ConsumeContext.RespondAsync``1(``0)" || d == "M:MassTransit.ConsumeContext.RespondAsync``1(System.Object)")
	case "message_publish":
		valid = assembly == "MassTransit.Abstractions" && strings.HasPrefix(d, "M:MassTransit.IPublishEndpoint.Publish``1(")
	case "message_consumer":
		valid = assembly == "MassTransit.Abstractions" && d == "T:MassTransit.IConsumer`1"
	case "message_publish_configuration":
		valid = assembly == "MassTransit" && d == "M:MassTransit.PublishExtensions.Publish``3(MassTransit.EventActivityBinder{``0,``1},MassTransit.EventMessageFactory{``0,``1,``2},System.Action{MassTransit.PublishContext{``2}})"
	case "message_event_configuration":
		valid = assembly == "MassTransit" && d == "M:MassTransit.MassTransitStateMachine`1.Event``1(System.Linq.Expressions.Expression{System.Func{MassTransit.Event{``0}}},System.Action{MassTransit.IEventCorrelationConfigurator{`0,``0}})"
	case "storage_entity":
		valid = assembly == "Microsoft.EntityFrameworkCore" && d == "T:Microsoft.EntityFrameworkCore.DbSet`1"
	case "storage_entity_use":
		valid = assembly == "Microsoft.EntityFrameworkCore" && d == "M:Microsoft.EntityFrameworkCore.DbContext.Set``1"
	case "storage_entity_mapping":
		valid = assembly == "Microsoft.EntityFrameworkCore" && d == "M:Microsoft.EntityFrameworkCore.ModelBuilder.Entity``1"
	case "storage_context_registration":
		valid = assembly == "Microsoft.EntityFrameworkCore" && d == "M:Microsoft.Extensions.DependencyInjection.EntityFrameworkServiceCollectionExtensions.AddDbContext``1(Microsoft.Extensions.DependencyInjection.IServiceCollection,System.Action{Microsoft.EntityFrameworkCore.DbContextOptionsBuilder},Microsoft.Extensions.DependencyInjection.ServiceLifetime,Microsoft.Extensions.DependencyInjection.ServiceLifetime)"
	case "storage_table":
		prefix := "M:Microsoft.EntityFrameworkCore.RelationalEntityTypeBuilderExtensions.ToTable``1(Microsoft.EntityFrameworkCore.Metadata.Builders.EntityTypeBuilder{``0},System.String"
		valid = assembly == "Microsoft.EntityFrameworkCore.Relational" && (d == prefix+")" || d == prefix+",System.String)")
	}
	if !valid {
		return fmt.Errorf("semantic: domain fact does not match bound framework API")
	}
	return nil
}

// ValidateDomainTarget prevents erased constructed/open generic targets from
// masquerading as concrete types in the supported domain rules.
func ValidateDomainTarget(s Symbol) error {
	k := s.Key
	if k.Language != "csharp" || k.DescriptorKind != "documentation_comment_id" || !strings.HasPrefix(k.Descriptor, "T:") || strings.ContainsAny(k.Descriptor, "`{}[]") {
		return fmt.Errorf("semantic: domain target is not a supported concrete named type")
	}
	return nil
}

func validKeyedAPI(s Symbol, lifetime string) bool {
	k := s.Key
	name := map[string]string{"transient": "AddKeyedTransient", "scoped": "AddKeyedScoped", "singleton": "AddKeyedSingleton"}[lifetime]
	return name != "" && k.Language == "csharp" && k.NamespaceKind == "assembly" && strings.SplitN(k.Namespace, ",", 2)[0] == "Microsoft.Extensions.DependencyInjection.Abstractions" && k.DescriptorKind == "documentation_comment_id" && k.Descriptor == "M:Microsoft.Extensions.DependencyInjection.ServiceCollectionServiceExtensions."+name+"``2(Microsoft.Extensions.DependencyInjection.IServiceCollection,System.Object)"
}
func ValidateDomainFactTarget(f DomainFact, role string, s Symbol) error {
	if f.Kind == "routing_slip_activity_configuration" {
		k := s.Key
		if role == "formatter_api" {
			if k.Language == "csharp" && k.NamespaceKind == "assembly" && strings.SplitN(k.Namespace, ",", 2)[0] == "MassTransit.Abstractions" && k.DescriptorKind == "documentation_comment_id" && k.Descriptor == "M:MassTransit.IEndpointNameFormatter.ExecuteActivity``2" {
				return nil
			}
			return fmt.Errorf("semantic: invalid routing formatter")
		}
		if role == "address_field" {
			if k.Language == "csharp" && k.NamespaceKind == "project" && k.DescriptorKind == "documentation_comment_id" && strings.HasPrefix(k.Descriptor, "F:") && strings.LastIndex(k.Descriptor, ".") > 2 && !strings.ContainsAny(k.Descriptor, "`{}[]#()") {
				return nil
			}
			return fmt.Errorf("semantic: invalid routing address field")
		}
	}

	if f.Kind == "di_open_generic_registration_configuration" {
		if role != "service_template" && role != "implementation_template" {
			return fmt.Errorf("semantic: invalid open DI role")
		}
		_, err := OpenGenericTemplateArity(s)
		return err
	}
	if f.Kind == "mediator_send_configuration" && role == "response" && s.Key.DescriptorKind == "constructed_named_type_v1" {
		return ValidateConstructedNamedType(s.Key)
	}
	if f.Kind == "endpoint_post_configuration" && role == "handler" {
		k := s.Key
		head, _, _ := strings.Cut(k.Descriptor, "(")
		if k.Language == "csharp" && k.NamespaceKind == "project" && k.DescriptorKind == "documentation_comment_id" && strings.HasPrefix(head, "M:") && strings.LastIndex(head, ".") > 2 && !strings.ContainsAny(head, "`#") {
			return nil
		}
		return fmt.Errorf("semantic: endpoint handler requires a source non-generic method")
	}
	if role == "registration_api" && (f.Kind == "di_keyed_registration" || f.Kind == "di_keyed_registration_configuration") {
		if validKeyedAPI(s, f.Lifetime) {
			return nil
		}
		return fmt.Errorf("semantic: invalid keyed registration API witness")
	}
	return ValidateDomainTarget(s)
}
