using Microsoft.CodeAnalysis;
using Microsoft.CodeAnalysis.CSharp.Syntax;
using Microsoft.CodeAnalysis.Operations;

static partial class Worker
{
    // Record only the declared assembly witness. No handler enumeration,
    // container registration, callback execution or runtime selection is inferred.
    static object[]? MediatorScanFacts(SemanticModel model, InvocationExpressionSyntax invocation, IMethodSymbol method)
    {
        var definition = (method.ReducedFrom ?? method).OriginalDefinition;
        const string api = "M:Microsoft.Extensions.DependencyInjection.MediatRServiceConfiguration.RegisterServicesFromAssembly(System.Reflection.Assembly)";
        if (!FrameworkType(definition.ContainingType, "Microsoft.Extensions.DependencyInjection", "MediatRServiceConfiguration", "MediatR") ||
            definition.GetDocumentationCommentId() != api || model.GetOperation(invocation) is not IInvocationOperation operation ||
            operation.Arguments.Length != 1 || operation.Arguments[0].Value is not IPropertyReferenceOperation property ||
            property.Property.GetDocumentationCommentId() != "P:System.Type.Assembly" ||
            !FrameworkType(property.Property.ContainingType, "System", "Type", "System.Runtime") ||
            property.Instance is not ITypeOfOperation { TypeOperand: INamedTypeSymbol marker } || !DomainTarget(marker)) return null;
        return DomainFact("mediator_assembly_scan_configuration", [("assembly_marker", marker)], rule: "csharp-mediatr-scan-v1");
    }
}
