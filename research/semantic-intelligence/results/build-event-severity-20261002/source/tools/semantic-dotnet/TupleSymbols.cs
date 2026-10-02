using Microsoft.CodeAnalysis;

static partial class Worker
{
    // Tuple element names are source aliases for ValueTuple storage. Roslyn can
    // give those aliases source locations but System.Runtime assembly identity;
    // treating that combination as a missing source project aborts extraction.
    // Resolve the actual storage field, including the Rest chain for long tuples.
    static IFieldSymbol TupleStorageField(IFieldSymbol field)
    {
        var tuple = field.ContainingType;
        int index = -1;
        for (int i = 0; i < tuple.TupleElements.Length; i++)
        {
            var element = tuple.TupleElements[i];
            if (SymbolEqualityComparer.Default.Equals(element, field) ||
                SymbolEqualityComparer.Default.Equals(element.CorrespondingTupleField, field.CorrespondingTupleField))
            { index = i; break; }
        }
        if (index < 0 || tuple.TupleUnderlyingType is not { } storage)
            throw new InvalidDataException("cannot identify tuple storage field");
        while (index >= 7)
        {
            if (storage.GetMembers("Rest").OfType<IFieldSymbol>().SingleOrDefault()?.Type is not INamedTypeSymbol rest)
                throw new InvalidDataException("cannot identify tuple Rest storage");
            storage = rest.TupleUnderlyingType ?? rest;
            index -= 7;
        }
        return storage.GetMembers("Item" + (index + 1)).OfType<IFieldSymbol>().Single();
    }
}
