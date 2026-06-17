package read

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	pricingtypes "github.com/aws/aws-sdk-go-v2/service/pricing/types"

	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// pricingRegion is the canonical region for the AWS Pricing API. The service
// only responds in a handful of regions and us-east-1 is the historical
// default; we pin it here so the tool works regardless of the user's bound
// awsx.Client region.
const pricingRegion = "us-east-1"

// pricingClientFactory builds a Pricing client. The Pricing API only lives in
// a handful of regions, so we ignore the toolset's bound region and force
// pricingRegion.
var pricingClientFactory = func(ctx context.Context, toolName string) (pricingAPI, error) {
	c, err := tools.RequireAWSClient(ctx, toolName)
	if err != nil {
		return nil, err
	}
	cfg, err := tools.LoadAWSConfig(ctx, toolName, c)
	if err != nil {
		return nil, err
	}
	cfg.Region = pricingRegion
	return pricing.NewFromConfig(cfg), nil
}

// pricingAPI is the subset of Pricing operations the read tool calls.
type pricingAPI interface {
	GetProducts(ctx context.Context, in *pricing.GetProductsInput, opts ...func(*pricing.Options)) (*pricing.GetProductsOutput, error)
}

// getProduct wraps pricing:GetProducts. The AI uses it when answering cost
// questions ("how much is this instance class per hour"); cacheable so we
// don't beat the API on repeated questions in the same session.
type getProduct struct{}

// Name reports the catalogue name.
func (getProduct) Name() string { return "pricing/get-product" }

// Permission returns the const PermissionRead.
func (getProduct) Permission() tools.Permission { return tools.PermissionRead }

// Schema declares the args.
func (t getProduct) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Fetch raw AWS Pricing entries (each PriceListItem is a JSON-encoded string the LLM can parse). Pass service_code and any filters needed to narrow results.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service_code": map[string]any{"type": "string", "description": "Service code, e.g. AmazonEC2, AmazonRDS."},
				"filters": map[string]any{
					"type":        "object",
					"description": "Map of attribute name -> required value. Each entry becomes a TERM_MATCH filter.",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Maximum entries to return (default 10, hard cap 100).",
				},
			},
			"required": []string{"service_code"},
		},
	}
}

// Execute calls pricing:GetProducts and forwards the raw price-list JSON
// blobs unchanged — the LLM is in a much better position than this tool to
// pick the right currency / term / unit from the result.
func (t getProduct) Execute(ctx context.Context, args map[string]any) (any, error) {
	svc, err := tools.ArgString(t.Name(), args, "service_code", true)
	if err != nil {
		return nil, err
	}
	filtersRaw, err := tools.ArgMap(t.Name(), args, "filters", false)
	if err != nil {
		return nil, err
	}
	max, err := tools.ArgInt(t.Name(), args, "max_results", false)
	if err != nil {
		return nil, err
	}
	if max <= 0 {
		max = 10
	}
	if max > 100 {
		max = 100
	}
	filters := make([]pricingtypes.Filter, 0, len(filtersRaw))
	filters = append(filters, pricingtypes.Filter{
		Type:  pricingtypes.FilterTypeTermMatch,
		Field: aws.String("ServiceCode"),
		Value: aws.String(svc),
	})
	for k, v := range filtersRaw {
		s, ok := v.(string)
		if !ok {
			return nil, &tools.ToolError{
				Code: tools.ErrCodeBadArgs, Tool: t.Name(),
				Message: "filter values must be strings",
			}
		}
		filters = append(filters, pricingtypes.Filter{
			Type:  pricingtypes.FilterTypeTermMatch,
			Field: aws.String(k),
			Value: aws.String(s),
		})
	}
	api, err := pricingClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	out, err := api.GetProducts(ctx, &pricing.GetProductsInput{
		ServiceCode: aws.String(svc),
		Filters:     filters,
		MaxResults:  aws.Int32(int32(max)),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"format_version": aws.ToString(out.FormatVersion),
		"price_list":     out.PriceList,
	}, nil
}

func init() {
	tools.MustRegister(tools.Default, getProduct{})
}
