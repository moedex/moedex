using Microsoft.CodeAnalysis;
using System.Text.Json;

static partial class Worker
{
    // Only closed, non-nested interface containers with simple named arguments.
    // The qualified argument keys prevent same-name types from collapsing.
    static bool ClosedInterfaceMethod(IMethodSymbol method) =>
        !method.IsGenericMethod && method.ContainingType is { TypeKind: TypeKind.Interface, Arity: > 0, ContainingType: null } type &&
        type.TypeArguments.Length <= 8 && type.TypeArguments.All(DomainTarget) &&
        !SymbolEqualityComparer.Default.Equals(type, type.OriginalDefinition);

    // Keep v1 identities and default forwarding bounds unchanged. V2 adds one
    // bounded constructed named type level to ordinary abstract interface slots.
    static object? ExtendedInterfaceSymbol(IMethodSymbol method)
    {
        if (method.IsGenericMethod || method.MethodKind != MethodKind.Ordinary || !method.IsAbstract || method.IsStatic ||
            method.ContainingType is not { TypeKind: TypeKind.Interface, Arity: > 0, ContainingType: null } type ||
            type.TypeArguments.Length > 8 || type.TypeArguments.All(DomainTarget)) return null;
        var symbols = type.TypeArguments.Select(BoundedTypeSymbol).ToArray();
        if (symbols.Any(s => s is null)) return null;
        var arguments = symbols.Select(s => JsonSerializer.SerializeToElement(s, Json).EnumerateObject()
            .Where(p => p.Name is not ("display_name" or "kind"))
            .ToDictionary(p => p.Name, p => (object)p.Value)).ToArray();
        var descriptor = JsonSerializer.Serialize(new { Definition = method.OriginalDefinition.GetDocumentationCommentId(), Arguments = arguments }, Json);
        if (Utf8.GetByteCount(descriptor) > 32768) return null;
        var original = JsonSerializer.SerializeToElement(Symbol(method.OriginalDefinition), Json);
        var fields = original.EnumerateObject().ToDictionary(p => p.Name, p => (object)p.Value);
        fields["descriptor_kind"] = "constructed_interface_method_v2";
        fields["descriptor"] = descriptor;
        fields["display_name"] = method.ToDisplayString(SymbolDisplayFormat.CSharpErrorMessageFormat);
        return fields;
    }

    static object ConstructedInterfaceSymbol(IMethodSymbol method)
    {
        var original = JsonSerializer.SerializeToElement(Symbol(method.OriginalDefinition), Json);
        var fields = original.EnumerateObject().ToDictionary(p => p.Name, p => (object)p.Value);
        var arguments = method.ContainingType.TypeArguments.Select(t =>
        {
            var key = JsonSerializer.SerializeToElement(Symbol(t), Json);
            return key.EnumerateObject().Where(p => p.Name is not ("display_name" or "kind"))
                .ToDictionary(p => p.Name, p => (object)p.Value);
        }).ToArray();
        fields["descriptor_kind"] = "constructed_interface_method_v1";
        fields["descriptor"] = JsonSerializer.Serialize(new { Definition = method.OriginalDefinition.GetDocumentationCommentId(), Arguments = arguments }, Json);
        fields["display_name"] = method.ToDisplayString(SymbolDisplayFormat.CSharpErrorMessageFormat);
        return fields;
    }
}
