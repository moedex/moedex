using Microsoft.CodeAnalysis;
using Microsoft.CodeAnalysis.CSharp.Syntax;
using Microsoft.CodeAnalysis.Operations;

static partial class Worker
{
    // A namespace marker declares scan scope, not a discovered registration set.
    // Non-null predicates stay excluded rather than losing their filter semantics.
    static object[]? MassTransitScanFacts(SemanticModel model, InvocationExpressionSyntax invocation, IMethodSymbol method)
    {
        var definition = (method.ReducedFrom ?? method).OriginalDefinition;
        if (!FrameworkType(definition.ContainingType, "MassTransit", "RegistrationExtensions", "MassTransit") ||
            definition.Name is not ("AddConsumersFromNamespaceContaining" or "AddActivitiesFromNamespaceContaining") ||
            definition.GetDocumentationCommentId() != $"M:MassTransit.RegistrationExtensions.{definition.Name}``1(MassTransit.IRegistrationConfigurator,System.Func{{System.Type,System.Boolean}})" ||
            method.TypeArguments.Length != 1 || method.TypeArguments[0] is not INamedTypeSymbol marker ||
            !DomainTarget(marker) || marker.ContainingNamespace.IsGlobalNamespace ||
            model.GetOperation(invocation) is not IInvocationOperation operation) return null;
        var filter = operation.Arguments.SingleOrDefault(a => a.Parameter?.Type is INamedTypeSymbol t && t.TypeKind == TypeKind.Delegate);
        if (filter?.Value.ConstantValue is not { HasValue: true, Value: null }) return null;
        var kind = definition.Name == "AddConsumersFromNamespaceContaining"
            ? "consumer_namespace_scan_configuration" : "activity_namespace_scan_configuration";
        return DomainFact(kind, [("namespace_marker", marker)], rule: "csharp-masstransit-scan-v1");
    }
}
