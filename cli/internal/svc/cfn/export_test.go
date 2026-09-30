package cfn

import (
	"fmt"
	"sync/atomic"
)

// RegisterTestTypes adds resource types that misbehave on demand, for testing
// failures during updates and rollbacks:
//
//	AWS::Test::Boom    cannot be created
//	AWS::Test::Flaky   is created, but deleting it fails while failDeletes is above zero
//	AWS::Test::Sticky  is created; updating it in place fails while failUpdates is above zero
func RegisterTestTypes(failDeletes, failUpdates *atomic.Int32) {
	awsTypes["AWS::Test::Boom"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			return "", nil, fmt.Errorf("the boom resource always fails")
		},
	}
	awsTypes["AWS::Test::Flaky"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			return x.Stack + "-" + x.Logical + "-" + randID(8), map[string]any{}, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			if failDeletes.Load() > 0 {
				failDeletes.Add(-1)
				return fmt.Errorf("flaky delete failed")
			}
			return nil
		},
	}
	awsTypes["AWS::Test::Sticky"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			return x.Stack + "-" + x.Logical + "-" + randID(8), map[string]any{}, nil
		},
		Delete:  func(x *xctx, r *Resource) error { return nil },
		Mutable: []string{"Value"},
		Update: func(x *xctx, r *Resource, old, in map[string]any) (map[string]any, error) {
			if failUpdates.Load() > 0 {
				failUpdates.Add(-1)
				return nil, fmt.Errorf("sticky update failed")
			}
			return nil, nil
		},
	}
}
