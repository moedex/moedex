using Microsoft.EntityFrameworkCore;
using Shared;
namespace Storage;
public sealed class Store : DbContext
{
 public /*gold:entity*/DbSet<Notice> Notices => Set<Notice>();
 public void Configure(ModelBuilder model) => model.Entity<Notice>()./*gold:table*/ToTable("notices", "app");
}
