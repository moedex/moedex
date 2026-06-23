package eval

// TCSslApiGold is a seed gold set over the real Services.Registrar/TC.SslApi
// repo in ~/TCGitlab (C#). Every relevant RelPath was verified to actually
// contain the query terms (so the lexical ranker can fairly find it). Grades:
// 2 = the primary file named for the concept, 1 = related files.
//
// This is a real-corpus baseline for one language; the polyglot picture comes
// from FixtureGold. Multi-repo / TS / SQL / ColdFusion gold over ~/TCGitlab is
// future coverage — see tc-tech-stack memory.
func TCSslApiGold() []GoldQuery {
	return []GoldQuery{
		{Query: "refund order", Relevant: map[string]int{
			"src/TC.SslApi.Models/Dtos/RefundOrder.cs":        2,
			"src/TC.SslApi.Models/Events/SslOrderRefunded.cs": 1,
			"src/TC.SslApi.Models/Vendor/RefundRequest.cs":    1,
		}},
		{Query: "ssl monitor", Relevant: map[string]int{
			"src/TC.SslApi.Models/SslMonitor.cs":                  2,
			"src/TC.SslApi.Models/Dtos/GetSslMonitor.cs":          1,
			"src/TC.SslApi.Models/Dtos/UpsertSslMonitor.cs":       1,
			"src/TC.SslApi.Models/Events/SslMonitorTestFailed.cs": 1,
		}},
		{Query: "reissue certificate", Relevant: map[string]int{
			"src/TC.SslApi.Models/Dtos/ReIssueCertificate.cs":   2,
			"src/TC.SslApi.Models/Vendor/ReIssueCertificate.cs": 1,
		}},
		{Query: "change approver", Relevant: map[string]int{
			"src/TC.SslApi.Models/Dtos/ChangeApproverMethod.cs": 2,
			"src/TC.SslApi.Models/Vendor/ChangeApprover.cs":     2,
			"src/TC.SslApi.Models/Dtos/FetchApproverList.cs":    1,
		}},
		{Query: "download certificate", Relevant: map[string]int{
			"src/TC.SslApi.Models/Dtos/DownloadCertificate.cs":   2,
			"src/TC.SslApi.Models/Vendor/DownloadCertificate.cs": 2,
		}},
		{Query: "endpoint http method", Relevant: map[string]int{
			"src/TC.SslApi.Service/Vendors/TheSslStore/Attributes.cs": 2,
		}},
		{Query: "order status", Relevant: map[string]int{
			"src/TC.SslApi.Models/SslOrderStatus.cs":        2,
			"src/TC.SslApi.Models/Vendor/OrderStatus.cs":    2,
			"src/TC.SslApi.Models/Dtos/CheckOrderStatus.cs": 1,
		}},
	}
}
