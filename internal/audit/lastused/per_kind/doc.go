// Package perkind holds one composer per resource Kind that PR-01 of
// MVP-6 scans (see ADR-0040 for the kind list and ADR-0041 for the
// per-kind heuristic table). Each composer:
//
//   - Takes a small typed input struct (resource ID + any static
//     timestamps the scanner already extracted) plus the narrow
//     clients it needs to read CloudWatch metrics, log groups, ENIs,
//     etc.
//   - Builds a slice of [lastused.LastUsedSource] entries — including
//     ones with nil Value when a signal had no datapoints.
//   - Delegates Best / Confidence assembly + the universal
//     "signals disagree >30 d" detector to [lastused.Compose].
//
// Kind-specific concerns that go beyond the three generic source
// helpers (latest AMI referencing a snapshot, source-DB existence for
// an RDS snapshot, ECR image push/pull times, etc.) declare their own
// narrow client interfaces inside their per-kind file.
//
// No file in this package imports the AWS SDK.
package perkind
