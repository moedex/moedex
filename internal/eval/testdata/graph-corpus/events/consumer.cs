public record OrderSubmitted(int Id);

public class OrderConsumer : IConsumer<OrderSubmitted>
{
}
