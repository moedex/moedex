using Microsoft.CodeAnalysis;
using Microsoft.CodeAnalysis.CSharp.Syntax;
using Microsoft.CodeAnalysis.Operations;

static partial class Worker
{
    static object[]? RoutingSlipFacts(SemanticModel model, InvocationExpressionSyntax invocation, IMethodSymbol method)
    {
        var definition = (method.ReducedFrom ?? method).OriginalDefinition;
        if (!FrameworkType(definition.ContainingType, "MassTransit", "IItineraryBuilder", "MassTransit.Abstractions") ||
            definition.GetDocumentationCommentId() != "M:MassTransit.IItineraryBuilder.AddActivity(System.String,System.Uri,System.Object)" ||
            model.GetOperation(invocation) is not IInvocationOperation call) return null;
        var name = call.Arguments.SingleOrDefault(a => a.Parameter?.Ordinal == 0)?.Value;
        var address = call.Arguments.SingleOrDefault(a => a.Parameter?.Ordinal == 1)?.Value;
        if (name is not INameOfOperation { Syntax: InvocationExpressionSyntax label } || label.ArgumentList.Arguments.Count != 1 ||
            model.GetSymbolInfo(label.ArgumentList.Arguments[0].Expression).Symbol is not INamedTypeSymbol activity || !DomainTarget(activity) ||
            address is not IFieldReferenceOperation { Instance: IInstanceReferenceOperation { ReferenceKind: InstanceReferenceKind.ContainingTypeInstance } } fieldReference) return null;
        var field = fieldReference.Field;
        if (!field.IsReadOnly || field.IsStatic || field.DeclaredAccessibility != Accessibility.Private ||
            !DomainTarget(field.ContainingType) || field.DeclaringSyntaxReferences.Length != 1 ||
            field.DeclaringSyntaxReferences[0].GetSyntax() is not VariableDeclaratorSyntax { Initializer: null } ||
            model.GetEnclosingSymbol(invocation.SpanStart)?.ContainingType is not { } owner ||
            !SymbolEqualityComparer.Default.Equals(owner, field.ContainingType)) return null;
        // One source constructor, one field reference, and a direct top-level
        // assignment. This excludes aliases, ref escapes, branches and rewrites.
        var constructors = owner.InstanceConstructors;
        if (constructors.Length != 1 || constructors[0].DeclaringSyntaxReferences.Length != 1 ||
            constructors[0].DeclaringSyntaxReferences[0].GetSyntax() is not ConstructorDeclarationSyntax { Body: { } body } constructor) return null;
        if (body.Statements.Count > 32) return null;
        var nodes = constructor.DescendantNodes().Take(1025).ToArray();
        if (nodes.Length > 1024) return null;
        var ctorModel = model.Compilation.GetSemanticModel(constructor.SyntaxTree);
        var uses = nodes.OfType<IdentifierNameSyntax>()
            .Where(n => SymbolEqualityComparer.Default.Equals(ctorModel.GetSymbolInfo(n).Symbol, field)).Take(2).ToArray();
        if (uses.Length != 1) return null;
        var assignments = body.Statements.OfType<ExpressionStatementSyntax>()
            .Select(s => ctorModel.GetOperation(s.Expression)).OfType<ISimpleAssignmentOperation>()
            .Where(a => a.Target is IFieldReferenceOperation { Instance: IInstanceReferenceOperation { ReferenceKind: InstanceReferenceKind.ContainingTypeInstance } } f && SymbolEqualityComparer.Default.Equals(f.Field, field)).ToArray();
        if (assignments.Length != 1 || assignments[0].Value is not IObjectCreationOperation creation ||
            creation.Constructor is not { } uriCtor || !FrameworkType(uriCtor.ContainingType, "System", "Uri", "System.Runtime") ||
            uriCtor.GetDocumentationCommentId() != "M:System.Uri.#ctor(System.String)" || creation.Initializer is not null ||
            creation.Arguments.Length != 1 || creation.Arguments[0].Value is not IInterpolatedStringOperation text || text.Parts.Length != 2 ||
            text.Parts[0] is not IInterpolatedStringTextOperation prefix || prefix.Text.ConstantValue is not { HasValue: true, Value: "exchange:" } ||
            text.Parts[1] is not IInterpolationOperation { Alignment: null, FormatString: null, Expression: IInvocationOperation formatter }) return null;
        var api = formatter.TargetMethod;
        if (!FrameworkType(api.ContainingType, "MassTransit", "IEndpointNameFormatter", "MassTransit.Abstractions") ||
            api.OriginalDefinition.GetDocumentationCommentId() != "M:MassTransit.IEndpointNameFormatter.ExecuteActivity``2" ||
            api.TypeArguments.Length != 2 || !api.TypeArguments.All(DomainTarget) || formatter.Arguments.Length != 0 ||
            !SymbolEqualityComparer.Default.Equals(activity, api.TypeArguments[0])) return null;
        return DomainFact("routing_slip_activity_configuration", [("activity", activity), ("arguments", api.TypeArguments[1]),
            ("address_field", field), ("formatter_api", api)], rule: "csharp-routing-slip-v1");
    }
}
