//go:build !(moedex_simd && amd64 && goexperiment.simd)

// Default build (every architecture, including the darwin/arm64 dev box, and
// every CGO_ENABLED=0 static build): the intersection core is the pure-Go
// goIntersect. The native AVX2 kernel in setops_simd_amd64.go is compiled ONLY
// when all three of moedex_simd, amd64, and goexperiment.simd are set, so the
// default `go build ./...` and `go test ./...` never reference simd/archsimd
// and go.mod stays untouched. This file is the always-present pure-Go twin the
// research note (research/simd-kernel.md) and the task's parity/dependency
// constraints require.

package setops

func init() {
	intersectImpl = goIntersect
}
