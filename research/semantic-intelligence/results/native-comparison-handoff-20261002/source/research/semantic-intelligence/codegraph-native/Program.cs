using System.Reflection.Metadata;
using System.Reflection.PortableExecutable;
using System.Security.Cryptography;
using System.Text.Json;

// Metadata-only inspection: never load or execute the inspected assemblies.
if (args.Length != 2) throw new ArgumentException("Usage: Provenance CHECKOUT BINARY_DIRECTORY");
var root = Path.GetFullPath(args[0]);
var bin = Path.GetFullPath(args[1]);
string Hash(string path) => Convert.ToHexString(SHA256.HashData(File.ReadAllBytes(path))).ToLowerInvariant();
var assemblies = new List<object>();
foreach (var dll in Directory.GetFiles(bin, "TC.CodeGraph*.dll").Order(StringComparer.Ordinal))
{
    var pdb = Path.ChangeExtension(dll, ".pdb");
    if (!File.Exists(pdb)) { assemblies.Add(new { assembly = Path.GetFileName(dll), sha256 = Hash(dll), status = "missing-pdb" }); continue; }
    using var peStream = File.OpenRead(dll);
    using var pe = new PEReader(peStream);
    using var pdbStream = File.OpenRead(pdb);
    using var provider = MetadataReaderProvider.FromPortablePdbStream(pdbStream);
    var reader = provider.GetMetadataReader();
    var id = new BlobContentId(reader.DebugMetadataHeader!.Id);
    var links = pe.ReadDebugDirectory().Where(e => e.Type == DebugDirectoryEntryType.CodeView).ToArray();
    bool linked = links.Any(e => pe.ReadCodeViewDebugDirectoryData(e).Guid == id.Guid && e.Stamp == id.Stamp);
    var documents = new List<object>();
    foreach (var handle in reader.Documents)
    {
        var doc = reader.GetDocument(handle);
        var name = reader.GetString(doc.Name).Replace('\\', '/');
        // Map only a unique source-root suffix. Do not read arbitrary PDB paths.
        var marker = name.IndexOf("/src/", StringComparison.Ordinal);
        string? relative = marker >= 0 ? name[(marker + 1)..] : null;
        if (relative is null || relative.Contains("/../", StringComparison.Ordinal))
        { documents.Add(new { document = name, status = "unmapped" }); continue; }
        var path = Path.GetFullPath(Path.Combine(root, relative));
        if (!path.StartsWith(root + Path.DirectorySeparatorChar, StringComparison.Ordinal)) throw new InvalidDataException("source path escape");
        var algorithm = reader.GetGuid(doc.HashAlgorithm);
        var expected = reader.GetBlobBytes(doc.Hash);
        var generated = relative.Contains("/obj/", StringComparison.Ordinal);
        string status;
        string? actual = null;
        if (!File.Exists(path)) status = "missing";
        else
        {
            var bytes = File.ReadAllBytes(path);
            byte[]? hash = algorithm == new Guid("8829d00f-11b8-4213-878b-770e8597ac16") ? SHA256.HashData(bytes)
                : algorithm == new Guid("ff1816ec-aa5e-4d10-87f7-6f4963833460") ? SHA1.HashData(bytes) : null;
            actual = hash is null ? null : Convert.ToHexString(hash).ToLowerInvariant();
            status = hash is null ? "unsupported-hash" : hash.AsSpan().SequenceEqual(expected) ? "match" : "mismatch";
        }
        documents.Add(new { document = name, relative, generated, algorithm, expected = Convert.ToHexString(expected).ToLowerInvariant(), actual, status });
    }
    assemblies.Add(new { assembly = Path.GetFileName(dll), sha256 = Hash(dll), pdb_sha256 = Hash(pdb), pdb_linked = linked, documents });
}
Console.WriteLine(JsonSerializer.Serialize(new { schema = "codegraph-portable-pdb-audit-v1", checkout = root, binary_directory = bin, assemblies,
    limitations = new[] { "PDB identity linkage and document hashes are consistency evidence, not trusted build attestation.", "No host execution, runtime closure, private dependency provenance or service readiness is established.", "Documents absent from a PDB require separate source-roster review." } }, new JsonSerializerOptions { WriteIndented = true }));
