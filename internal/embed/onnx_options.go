package embed

import "fmt"

// ONNXOptions controls ONNX Runtime's CPU execution pools. Zero leaves a pool
// at the runtime default. Intra-op threads parallelize operators such as matrix
// multiplication; inter-op threads parallelize independent graph nodes.
type ONNXOptions struct {
	IntraOpThreads int
	InterOpThreads int
}

func (o ONNXOptions) validate() error {
	if o.IntraOpThreads < 0 {
		return fmt.Errorf("embed/onnx: intra-op threads must be non-negative, got %d", o.IntraOpThreads)
	}
	if o.InterOpThreads < 0 {
		return fmt.Errorf("embed/onnx: inter-op threads must be non-negative, got %d", o.InterOpThreads)
	}
	return nil
}
