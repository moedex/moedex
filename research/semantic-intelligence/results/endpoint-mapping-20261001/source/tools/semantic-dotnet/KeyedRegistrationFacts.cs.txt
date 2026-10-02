using Microsoft.CodeAnalysis;
using Microsoft.CodeAnalysis.CSharp.Syntax;
using Microsoft.CodeAnalysis.Operations;

static partial class Worker
{
    static bool KeyedAPI(IMethodSymbol method) =>
        FrameworkType(method.ContainingType, "Microsoft.Extensions.DependencyInjection", "ServiceCollectionServiceExtensions", "Microsoft.Extensions.DependencyInjection.Abstractions") &&
        method.Name is "AddKeyedTransient" or "AddKeyedScoped" or "AddKeyedSingleton" &&
        method.Arity == 2 && method.Parameters.Length == 2 &&
        method.Parameters[0].Type.ToDisplayString() == "Microsoft.Extensions.DependencyInjection.IServiceCollection" &&
        method.Parameters[1].Type.SpecialType == SpecialType.System_Object;

    static object[]? KeyedRegistrationFacts(State state, SemanticModel model, InvocationExpressionSyntax invocation, IMethodSymbol bound)
    {
        var definition = (bound.ReducedFrom ?? bound).OriginalDefinition;
        var operation = model.GetOperation(invocation) as IInvocationOperation;
        if (operation is null) return null;
        if (KeyedAPI(definition))
            return KeyedFact(operation, t => t, false);
        // One source helper level. No naming heuristic, recursive summaries,
        // arbitrary control flow, factories or data-flow evaluation.
        if (!definition.IsStatic || !definition.IsGenericMethod || definition.IsAsync || definition.Arity > 8 ||
            definition.DeclaringSyntaxReferences.Length != 1 || bound.TypeArguments.Any(t => !DomainTarget(t))) return null;
        if (definition.DeclaringSyntaxReferences[0].GetSyntax() is not MethodDeclarationSyntax { Body: { Statements.Count: > 0 } body } ||
            body.Statements[0] is not ExpressionStatementSyntax { Expression: InvocationExpressionSyntax registration }) return null;
        var owners = States.Where(s => s.Complete && s.Compilation is not null &&
            s.Compilation.Assembly.Identity.Equals(definition.ContainingAssembly.Identity) &&
            s.Compilation.SyntaxTrees.Contains(registration.SyntaxTree)).Take(2).ToArray();
        if (owners.Length != 1) return null;
        var owner = owners[0];
        var templateModel = owner.Compilation!.GetSemanticModel(registration.SyntaxTree);
        if (templateModel.GetOperation(registration) is not IInvocationOperation template || !KeyedAPI(template.TargetMethod.OriginalDefinition)) return null;
        var templateMethod = templateModel.GetDeclaredSymbol(body.Parent!) as IMethodSymbol;
        if (templateMethod is null) return null;
        ITypeSymbol? Substitute(ITypeSymbol t) => t is ITypeParameterSymbol p
            ? p.TypeParameterKind == TypeParameterKind.Method && SymbolEqualityComparer.Default.Equals(p.ContainingSymbol, templateMethod) && p.Ordinal < bound.TypeArguments.Length ? bound.TypeArguments[p.Ordinal] : null
            : DomainTarget(t) ? t : null;
        return KeyedFact(template, Substitute, true);
    }

    static object[]? KeyedFact(IInvocationOperation operation, Func<ITypeSymbol, ITypeSymbol?> substitute, bool helper)
    {
        var method = operation.TargetMethod;
        if (!KeyedAPI(method.OriginalDefinition) || method.TypeArguments.Length != 2) return null;
        var key = operation.Arguments.SingleOrDefault(a => a.Parameter?.Name == "serviceKey")?.Value;
        while (key is IConversionOperation { IsImplicit: true } conversion)
            key = conversion.Operand;
        if (key is not ITypeOfOperation typeOf) return null;
        var service = substitute(method.TypeArguments[0]);
        var implementation = substitute(method.TypeArguments[1]);
        var keyType = substitute(typeOf.TypeOperand);
        if (service is null || implementation is null || keyType is null || !DomainTarget(service) || !DomainTarget(implementation) || !DomainTarget(keyType)) return null;
        var lifetime = method.Name switch { "AddKeyedSingleton" => "singleton", "AddKeyedScoped" => "scoped", _ => "transient" };
        return DomainFact(helper ? "di_keyed_registration_configuration" : "di_keyed_registration",
            [("service", service), ("implementation", implementation), ("key_type", keyType), ("registration_api", method.OriginalDefinition)], lifetime: lifetime, rule: "csharp-keyed-di-v1");
    }
}
