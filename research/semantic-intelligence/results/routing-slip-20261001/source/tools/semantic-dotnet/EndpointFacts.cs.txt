using Microsoft.CodeAnalysis;
using Microsoft.CodeAnalysis.CSharp.Syntax;
using Microsoft.CodeAnalysis.Operations;

static partial class Worker
{
    static object[]? EndpointFacts(SemanticModel model, InvocationExpressionSyntax invocation, IMethodSymbol method)
    {
        var definition = (method.ReducedFrom ?? method).OriginalDefinition;
        const string api = "M:Microsoft.AspNetCore.Builder.EndpointRouteBuilderExtensions.MapPost(Microsoft.AspNetCore.Routing.IEndpointRouteBuilder,System.String,System.Delegate)";
        if (!FrameworkType(definition.ContainingType, "Microsoft.AspNetCore.Builder", "EndpointRouteBuilderExtensions", "Microsoft.AspNetCore.Routing") ||
            definition.GetDocumentationCommentId() != api || model.GetOperation(invocation) is not IInvocationOperation operation) return null;
        var pattern = operation.Arguments.SingleOrDefault(a => a.Parameter?.Name == "pattern")?.Value.ConstantValue;
        if (pattern is not { HasValue: true, Value: string text } || string.IsNullOrWhiteSpace(text) || !DomainScalar(text)) return null;
        IOperation? handler = operation.Arguments.SingleOrDefault(a => a.Parameter?.Name == "handler")?.Value;
        // Unwrap only compiler conversions and delegate creation. Never follow
        // variables, properties, invocations or arbitrary expression data flow.
        for (int i = 0; i < 4; i++)
        {
            if (handler is IConversionOperation { OperatorMethod: null } conversion && !conversion.Conversion.IsUserDefined)
                handler = conversion.Operand;
            else if (handler is IDelegateCreationOperation creation)
                handler = creation.Target;
            else break;
        }
        if (handler is not IMethodReferenceOperation reference) return null;
        var target = reference.Method;
        if (!target.IsStatic || target.MethodKind != MethodKind.Ordinary || target.Arity != 0 ||
            !DomainTarget(target.ContainingType) || target.DeclaringSyntaxReferences.Length != 1 ||
            target.DeclaringSyntaxReferences[0].GetSyntax() is not MethodDeclarationSyntax ||
            !target.Locations.Any(l => l.IsInSource) || Symbol(target) is null) return null;
        // Pattern is the argument at this call site, not a composed group path.
        return DomainFact("endpoint_post_configuration", [("handler", target)], rule: "csharp-endpoint-v1", routePattern: text);
    }
}
