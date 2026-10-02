using MassTransit;
using Shared;
namespace Consumers;
public sealed class Consumer : /*gold:consumer*/IConsumer<Notice>
{
 public Task Consume(ConsumeContext<Notice> context) => Task.CompletedTask;
}
