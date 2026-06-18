package errors_test

import (
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/internal/errors"
)

// catalogueCase pairs a catalogue entry id with one representative raw
// error message + Context. The Match call must hit that entry, and the
// rendered AppError is sanity-checked for the expected substrings so a
// regex change in the YAML cannot silently turn a hit into a fallback.
type catalogueCase struct {
	name      string
	id        string
	raw       string
	ctx       errors.Context
	wantInRaw []string // substrings that must appear in the rendered fields
}

func cases() []catalogueCase {
	return []catalogueCase{
		{
			name: "iam-capability-missing",
			id:   "iam-capability-missing",
			raw:  "Requires capabilities : [CAPABILITY_NAMED_IAM]",
			ctx: errors.Context{
				AWSService: "CloudFormation",
				AWSCode:    "InsufficientCapabilitiesException",
				StackName:  "my-app",
				Region:     "us-east-1",
			},
			wantInRaw: []string{"CAPABILITY_NAMED_IAM"},
		},
		{
			name: "tg-name-collision",
			id:   "tg-name-collision",
			raw:  "Target group with name 'tg-api' already exists",
			ctx: errors.Context{
				AWSService: "ElasticLoadBalancingV2",
				AWSCode:    "DuplicateTargetGroupName",
				Region:     "eu-west-1",
			},
			wantInRaw: []string{"tg-api"},
		},
		{
			name: "subnet-az-mismatch",
			id:   "subnet-az-mismatch",
			raw:  "At least two subnets in two or more Availability Zones must be specified",
			ctx: errors.Context{
				AWSService: "ElasticLoadBalancingV2",
				Region:     "us-east-1",
				Inputs:     map[string]any{"VpcId": "vpc-abc"},
			},
			wantInRaw: []string{"vpc-abc"},
		},
		{
			name: "cert-region-mismatch",
			id:   "cert-region-mismatch",
			raw:  "Certificate 'arn:aws:acm:us-west-2:123456789012:certificate/abc' was not found",
			ctx: errors.Context{
				AWSService: "ElasticLoadBalancingV2",
				Region:     "us-east-1",
			},
			wantInRaw: []string{"us-west-2"},
		},
		{
			name: "vpc-limit-exceeded",
			id:   "vpc-limit-exceeded",
			raw:  "VpcLimitExceeded: maximum number of VPCs has been reached",
			ctx: errors.Context{
				AWSService: "EC2",
				AWSCode:    "VpcLimitExceeded",
				Region:     "us-east-1",
			},
			wantInRaw: []string{"us-east-1"},
		},
		{
			name: "parameters-mismatch",
			id:   "parameters-mismatch",
			raw:  "Parameters: [OldName] does not exist in the template",
			ctx: errors.Context{
				AWSService: "CloudFormation",
				AWSCode:    "ValidationError",
				StackName:  "my-stack",
				Region:     "us-east-1",
			},
			wantInRaw: []string{"OldName"},
		},
		{
			name: "rollback-complete",
			id:   "rollback-complete",
			raw:  "my-stack-name is in ROLLBACK_COMPLETE state and can not be updated.",
			ctx: errors.Context{
				AWSService: "CloudFormation",
				AWSCode:    "ValidationError",
				StackName:  "my-stack-name",
				Region:     "us-east-1",
			},
			wantInRaw: []string{"my-stack-name"},
		},
		{
			name: "delete-failed-eni",
			id:   "delete-failed-eni",
			raw:  "Network interface 'eni-0a1b2c3d4e5f6' is currently in use.",
			ctx: errors.Context{
				AWSService: "EC2",
				StackName:  "my-stack",
				Region:     "us-east-1",
			},
			wantInRaw: []string{"eni-0a1b2c3d4e5f6"},
		},
		{
			name: "ecs-task-definition-invalid",
			id:   "ecs-task-definition-invalid",
			raw:  "Task definition is not valid: cpu must be 256, 512, 1024, 2048, or 4096 with Fargate.",
			ctx: errors.Context{
				AWSService: "ECS",
				AWSCode:    "ClientException",
				Region:     "us-east-1",
			},
			wantInRaw: []string{"Fargate"},
		},
		{
			name: "iam-role-missing-permission",
			id:   "iam-role-missing-permission",
			raw:  "User: arn:aws:sts::123456789012:assumed-role/deploy-role/session is not authorized to perform: ec2:CreateVpc on resource: arn:aws:ec2:us-east-1:123456789012:vpc/*",
			ctx: errors.Context{
				AWSService: "CloudFormation",
				Region:     "us-east-1",
			},
			wantInRaw: []string{"ec2:CreateVpc"},
		},
		{
			name: "pipeline-source-unauthorized",
			id:   "pipeline-source-unauthorized",
			raw:  "OAuth token is unauthorized",
			ctx: errors.Context{
				AWSService: "CodePipeline",
				Region:     "us-east-1",
			},
			wantInRaw: []string{"us-east-1"},
		},
		{
			name: "cert-arn-format-invalid",
			id:   "cert-arn-format-invalid",
			raw:  "arn:aws:acm:bogus is not a valid certificate ARN",
			ctx: errors.Context{
				AWSService: "ElasticLoadBalancingV2",
				Region:     "us-east-1",
			},
			wantInRaw: []string{"us-east-1"},
		},
		{
			name: "vpc-peering-cross-vpc",
			id:   "vpc-peering-cross-vpc",
			raw:  "subnets must be in the same VPC",
			ctx: errors.Context{
				AWSService: "EC2",
				Region:     "us-east-1",
			},
			wantInRaw: []string{"us-east-1"},
		},
		{
			name: "waf-wcu-limit",
			id:   "waf-wcu-limit",
			raw:  "The web ACL exceeds the WCU limit",
			ctx: errors.Context{
				AWSService: "WAFv2",
				AWSCode:    "WAFLimitsExceededException",
				Region:     "us-east-1",
			},
			wantInRaw: []string{"us-east-1"},
		},
		{
			name: "healthcheck-5xx",
			id:   "healthcheck-5xx",
			raw:  "Health checks failed with these codes: [502]",
			ctx: errors.Context{
				AWSService: "ElasticLoadBalancingV2",
				Region:     "us-east-1",
				Inputs:     map[string]any{"HealthCheckPath": "/healthz"},
			},
			wantInRaw: []string{"/healthz"},
		},
		{
			name: "validate-template-unknown-resource-type",
			id:   "validate-template-unknown-resource-type",
			raw:  "Unknown resource type: 'AWS::Banana::Plant'",
			ctx: errors.Context{
				AWSService: "CloudFormation",
				AWSCode:    "ValidationError",
				Region:     "us-east-1",
			},
			wantInRaw: []string{"AWS::Banana::Plant"},
		},
		{
			name: "validate-template-unresolved-dependencies",
			id:   "validate-template-unresolved-dependencies",
			raw:  "Unresolved resource dependencies [WebVpc] in the Resources block of the template",
			ctx: errors.Context{
				AWSService: "CloudFormation",
				AWSCode:    "ValidationError",
				Region:     "us-east-1",
			},
			wantInRaw: []string{"WebVpc"},
		},
		{
			name: "validate-template-format-error",
			id:   "validate-template-format-error",
			raw:  "Template format error: At least one Resources member must be defined",
			ctx: errors.Context{
				AWSService: "CloudFormation",
				AWSCode:    "ValidationError",
				Region:     "us-east-1",
			},
			wantInRaw: []string{"At least one Resources member"},
		},
		{
			name: "validate-template-invalid-property",
			id:   "validate-template-invalid-property",
			raw:  "Encountered unsupported property BucketNme",
			ctx: errors.Context{
				AWSService: "CloudFormation",
				AWSCode:    "ValidationError",
				Region:     "us-east-1",
			},
			wantInRaw: []string{"BucketNme"},
		},
		{
			name: "validate-template-too-large",
			id:   "validate-template-too-large",
			raw:  "Member must have length less than or equal to 51200",
			ctx: errors.Context{
				AWSService: "CloudFormation",
				AWSCode:    "ValidationError",
				Region:     "us-east-1",
			},
			wantInRaw: []string{"51,200-byte"},
		},
	}
}

// TestMatchEveryCatalogueEntry walks the table and asserts that each
// representative raw error hits its expected catalogue id with the
// expected substring landing in the rendered output. The test also
// guarantees the table covers every entry — adding a 16th YAML without a
// test case here is a build-time error rather than a silent omission.
func TestMatchEveryCatalogueEntry(t *testing.T) {
	entries, err := errors.LoadCatalogue()
	if err != nil {
		t.Fatalf("LoadCatalogue: %v", err)
	}

	tcs := cases()
	if len(tcs) < 15 {
		t.Fatalf("test table must cover at least 15 catalogue entries, got %d", len(tcs))
	}

	caseByID := map[string]catalogueCase{}
	for _, c := range tcs {
		if _, dup := caseByID[c.id]; dup {
			t.Fatalf("test table has duplicate id %q", c.id)
		}
		caseByID[c.id] = c
	}
	for _, e := range entries {
		if _, ok := caseByID[e.ID]; !ok {
			t.Errorf("catalogue entry %q has no test case", e.ID)
		}
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			got := errors.MatchString(tc.raw, tc.ctx)
			if got == nil {
				t.Fatalf("Match returned nil")
			}
			if got.MatchedID != tc.id {
				t.Fatalf("Match landed on %q, want %q\n  raw: %s\n  rendered title: %s",
					got.MatchedID, tc.id, tc.raw, got.Title)
			}
			if got.Title == "" {
				t.Errorf("Title is empty")
			}
			if got.Cause == "" {
				t.Errorf("Cause is empty")
			}
			if len(got.Suggested) == 0 {
				t.Errorf("Suggested is empty")
			}
			if got.Raw != tc.raw {
				t.Errorf("Raw not threaded through: got %q, want %q", got.Raw, tc.raw)
			}
			rendered := got.Title + "\n" + got.Cause + "\n" + got.ConsoleURL + "\n" + strings.Join(got.Suggested, "\n")
			for _, sub := range tc.wantInRaw {
				if !strings.Contains(rendered, sub) {
					t.Errorf("rendered output missing %q\nfull:\n%s", sub, rendered)
				}
			}
		})
	}
}

// TestMatchFallback exercises the unknown-error path: a Match call with no
// catalogue hit returns Raw populated and nothing else.
func TestMatchFallback(t *testing.T) {
	raw := "totally unrecognised AWS babble from the future"
	got := errors.MatchString(raw, errors.Context{
		AWSService: "FutureService",
		AWSCode:    "MysteryError",
		StackName:  "x",
	})
	if got == nil {
		t.Fatal("Match returned nil")
	}
	if got.MatchedID != "" {
		t.Errorf("expected fallback (empty MatchedID), got %q", got.MatchedID)
	}
	if got.Title != "" || got.Cause != "" || got.ConsoleURL != "" {
		t.Errorf("fallback should leave templated fields empty, got %+v", got)
	}
	if got.Raw != raw {
		t.Errorf("Raw not threaded through: got %q", got.Raw)
	}
	if got.AWSCode != "MysteryError" {
		t.Errorf("AWSCode not threaded through")
	}
}

// TestMatchNilError accepts a nil error gracefully.
func TestMatchNilError(t *testing.T) {
	got := errors.Match(nil, errors.Context{})
	if got == nil {
		t.Fatal("Match(nil) returned nil")
	}
	if got.MatchedID != "" {
		t.Errorf("expected fallback, got matched id %q", got.MatchedID)
	}
	if got.Raw != "" {
		t.Errorf("nil error should produce empty Raw, got %q", got.Raw)
	}
}

// TestMatchPriorityOrder asserts the loader sorted entries by descending
// priority so the catalogue authoring contract (higher priority wins) is
// enforced at load time, not just by Match's first-match-wins loop.
func TestMatchPriorityOrder(t *testing.T) {
	entries, err := errors.LoadCatalogue()
	if err != nil {
		t.Fatalf("LoadCatalogue: %v", err)
	}
	if len(entries) < 2 {
		t.Skip("not enough entries to compare ordering")
	}
	for i := 1; i < len(entries); i++ {
		prev, cur := entries[i-1], entries[i]
		if prev.Priority < cur.Priority {
			t.Errorf("entries not sorted: %s (p=%d) appears before %s (p=%d)",
				prev.ID, prev.Priority, cur.ID, cur.Priority)
		}
	}
}

// TestMatchAWSServiceFilterMismatch makes sure aws_service is enforced —
// the rollback-complete entry should not match if the service is wrong.
func TestMatchAWSServiceFilterMismatch(t *testing.T) {
	raw := "my-stack is in ROLLBACK_COMPLETE state and can not be updated"
	got := errors.MatchString(raw, errors.Context{
		AWSService: "WrongService",
		AWSCode:    "ValidationError",
	})
	if got.MatchedID == "rollback-complete" {
		t.Errorf("aws_service filter ignored — matched rollback-complete with WrongService")
	}
}
