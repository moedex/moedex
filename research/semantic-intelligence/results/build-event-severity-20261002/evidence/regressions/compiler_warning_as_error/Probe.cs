#warning promoted warning
public class TupleProbe {
    public string Read((string Summary, string Description) value) => value.Summary + value.Item1 + value.Description;
    public int Long((int A, int B, int C, int D, int E, int F, int G, int H, int I) value) => value.H + value.Item8 + value.I;
    public int Plain { get; set; }
}
