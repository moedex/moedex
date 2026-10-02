using Microsoft.CodeAnalysis;
using Microsoft.CodeAnalysis.CSharp.Syntax;
using Microsoft.CodeAnalysis.Operations;

static partial class Worker
{
    // Positional open registration templates, not constructed services or proof
    // that constraints, constructors, registration order or activation succeed.
    static object[]? OpenGenericRegistrationFacts(SemanticModel model, InvocationExpressionSyntax invocation, IMethodSymbol method)
    {
        var definition = (method.ReducedFrom ?? method).OriginalDefinition;
        if (!FrameworkType(definition.ContainingType, "Microsoft.Extensions.DependencyInjection", "ServiceCollectionServiceExtensions", "Microsoft.Extensions.DependencyInjection.Abstractions") ||
            definition.Name is not ("AddSingleton" or "AddScoped" or "AddTransient") ||
            definition.GetDocumentationCommentId() != $"M:Microsoft.Extensions.DependencyInjection.ServiceCollectionServiceExtensions.{definition.Name}(Microsoft.Extensions.DependencyInjection.IServiceCollection,System.Type,System.Type)" ||
            model.GetOperation(invocation) is not IInvocationOperation operation) return null;
        INamedTypeSymbol? Template(string parameter)
        {
            var argument = operation.Arguments.SingleOrDefault(a => a.Parameter?.Name == parameter)?.Value;
            return argument is ITypeOfOperation { TypeOperand: INamedTypeSymbol { IsUnboundGenericType: true, ContainingType: null, Arity: > 0 and <= 8 } type }
                ? type.OriginalDefinition : null;
        }
        var service = Template("serviceType");
        var implementation = Template("implementationType");
        if (service is not { TypeKind: TypeKind.Interface } ||
            implementation is not { TypeKind: TypeKind.Class, IsAbstract: false, IsStatic: false } ||
            service.Arity != implementation.Arity) return null;
        // Identity and position, never names: I<A,B> must be implemented as I<T,U>
        // for C<T,U>. Swaps, repetitions, constants and nested substitutions fail.
        if (!implementation.AllInterfaces.Any(i => SymbolEqualityComparer.Default.Equals(i.OriginalDefinition, service) &&
            i.TypeArguments.Length == implementation.Arity && i.TypeArguments.Select((a, n) =>
                SymbolEqualityComparer.Default.Equals(a, implementation.TypeParameters[n])).All(equal => equal))) return null;
        var lifetime = definition.Name switch { "AddSingleton" => "singleton", "AddScoped" => "scoped", _ => "transient" };
        return DomainFact("di_open_generic_registration_configuration",
            [("service_template", service), ("implementation_template", implementation)],
            lifetime: lifetime, rule: "csharp-open-di-v1");
    }
}
