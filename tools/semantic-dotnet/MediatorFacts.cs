using Microsoft.CodeAnalysis;
using Microsoft.CodeAnalysis.CSharp.Syntax;
using Microsoft.CodeAnalysis.Operations;
using System.Text.Json;

static partial class Worker
{
    static object[]? MediatorFacts(SemanticModel model, InvocationExpressionSyntax invocation, IMethodSymbol method)
    {
        var definition = (method.ReducedFrom ?? method).OriginalDefinition;
        const string api = "M:MediatR.ISender.Send``1(MediatR.IRequest{``0},System.Threading.CancellationToken)";
        if (!FrameworkType(definition.ContainingType, "MediatR", "ISender", "MediatR") ||
            definition.GetDocumentationCommentId() != api || method.TypeArguments.Length != 1 ||
            model.GetOperation(invocation) is not IInvocationOperation operation) return null;
        IOperation? request = operation.Arguments.SingleOrDefault(a => a.Parameter?.Name == "request")?.Value;
        // A direct construction witnesses the request type. Do not guess through
        // variables, factories, explicit casts, or interface-typed references.
        for (int i = 0; i < 4 && request is IConversionOperation { IsImplicit: true, OperatorMethod: null } conversion && !conversion.Conversion.IsUserDefined; i++)
            request = conversion.Operand;
        if (request is not IObjectCreationOperation { Type: INamedTypeSymbol requestType } || !DomainTarget(requestType)) return null;
        var response = method.TypeArguments[0];
        if (!requestType.AllInterfaces.Any(i => FrameworkType(i, "MediatR", "IRequest`1", "MediatR.Contracts") &&
            i.TypeArguments.Length == 1 && SymbolEqualityComparer.Default.Equals(i.TypeArguments[0], response))) return null;
        var responseSymbol = BoundedTypeSymbol(response);
        if (responseSymbol is null) return null;
        return [new { Kind = "mediator_send_configuration", Rule = "csharp-mediatr-v1", EvidenceScope = "compile_time",
            Targets = new[] { new { Role = "request", Symbol = Symbol(requestType) }, new { Role = "response", Symbol = (object?)responseSymbol } } }];
    }

    // One closed generic level, up to eight independently qualified simple named
    // arguments. Shared by response facts and closed interface correspondence.
    static object? BoundedTypeSymbol(ITypeSymbol response)
    {
        if (DomainTarget(response)) return Symbol(response);
        if (response is not INamedTypeSymbol { ContainingType: null, Arity: > 0 } type || type.IsTupleType ||
            type.TypeArguments.Length > 8 || !type.TypeArguments.All(DomainTarget)) return null;
        var original = JsonSerializer.SerializeToElement(Symbol(type.OriginalDefinition), Json);
        var fields = original.EnumerateObject().ToDictionary(p => p.Name, p => (object)p.Value);
        var arguments = type.TypeArguments.Select(t => JsonSerializer.SerializeToElement(Symbol(t), Json).EnumerateObject()
            .Where(p => p.Name is not ("display_name" or "kind"))
            .ToDictionary(p => p.Name, p => (object)p.Value)).ToArray();
        var descriptor = JsonSerializer.Serialize(new { Definition = type.OriginalDefinition.GetDocumentationCommentId(), Arguments = arguments }, Json);
        if (Utf8.GetByteCount(descriptor) > 32768) return null;
        fields["descriptor_kind"] = "constructed_named_type_v1";
        fields["descriptor"] = descriptor;
        fields["display_name"] = type.ToDisplayString(SymbolDisplayFormat.CSharpErrorMessageFormat);
        return fields;
    }
}
