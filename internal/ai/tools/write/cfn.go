package write

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// cfnClientFactory builds a CloudFormation client bound to the toolset's
// awsx.Client. Replaceable in tests.
var cfnClientFactory = func(ctx context.Context, toolName string) (cfnAPI, error) {
	c, err := tools.RequireAWSClient(ctx, toolName)
	if err != nil {
		return nil, err
	}
	cfg, err := tools.LoadAWSConfig(ctx, toolName, c)
	if err != nil {
		return nil, err
	}
	return cloudformation.NewFromConfig(cfg), nil
}

// cfnAPI is the subset of CloudFormation write operations the tools call.
type cfnAPI interface {
	UpdateStack(ctx context.Context, in *cloudformation.UpdateStackInput, opts ...func(*cloudformation.Options)) (*cloudformation.UpdateStackOutput, error)
	CreateStack(ctx context.Context, in *cloudformation.CreateStackInput, opts ...func(*cloudformation.Options)) (*cloudformation.CreateStackOutput, error)
	DeleteStack(ctx context.Context, in *cloudformation.DeleteStackInput, opts ...func(*cloudformation.Options)) (*cloudformation.DeleteStackOutput, error)
	CancelUpdateStack(ctx context.Context, in *cloudformation.CancelUpdateStackInput, opts ...func(*cloudformation.Options)) (*cloudformation.CancelUpdateStackOutput, error)
}

// parametersFromArgs converts a {key: value} map into CFN Parameter inputs.
// Values may be strings, numbers, or bools; everything stringifies cleanly.
func parametersFromArgs(toolName string, m map[string]any) ([]cfntypes.Parameter, error) {
	if len(m) == 0 {
		return nil, nil
	}
	out := make([]cfntypes.Parameter, 0, len(m))
	for k, v := range m {
		s, err := tools.ArgString(toolName, map[string]any{"_": v}, "_", true)
		if err != nil {
			return nil, &tools.ToolError{
				Code: tools.ErrCodeBadArgs, Tool: toolName,
				Message: "parameters[" + k + "] must be a string or scalar",
			}
		}
		out = append(out, cfntypes.Parameter{
			ParameterKey:   aws.String(k),
			ParameterValue: aws.String(s),
		})
	}
	return out, nil
}

// capabilitiesFromArgs maps a string slice into CFN Capability enum values.
// Unknown capabilities are passed through verbatim — the SDK will reject
// them server-side which is the right behaviour.
func capabilitiesFromArgs(in []string) []cfntypes.Capability {
	if len(in) == 0 {
		return nil
	}
	out := make([]cfntypes.Capability, 0, len(in))
	for _, c := range in {
		out = append(out, cfntypes.Capability(c))
	}
	return out
}

// updateStack issues cloudformation:UpdateStack with caller-supplied
// parameter overrides. PR-04 will render a diff in the consent modal before
// the call actually goes through.
type updateStack struct{}

// Name reports the catalogue name.
func (updateStack) Name() string { return "cfn/update-stack" }

// Permission returns the const PermissionWrite.
func (updateStack) Permission() tools.Permission { return tools.PermissionWrite }

// Schema declares the args.
func (t updateStack) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Update an existing CloudFormation stack with new parameter values. Either use_previous_template or template_body / template_url must be set.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"stack_name":            map[string]any{"type": "string", "description": "Stack name or ARN."},
				"use_previous_template": map[string]any{"type": "boolean", "description": "Reuse the stack's current template."},
				"template_body":         map[string]any{"type": "string", "description": "Inline template (mutually exclusive with template_url and use_previous_template)."},
				"template_url":          map[string]any{"type": "string", "description": "S3 URL to a template."},
				"parameters":            map[string]any{"type": "object", "description": "Map of parameter key -> value."},
				"capabilities":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "IAM/NAMED_IAM/AUTO_EXPAND as required."},
				"reason":                map[string]any{"type": "string", "description": "Why this update is needed — surfaced in the consent modal."},
			},
			"required": []string{"stack_name", "reason"},
		},
	}
}

// Execute issues UpdateStack.
func (t updateStack) Execute(ctx context.Context, args map[string]any) (any, error) {
	name, err := tools.ArgString(t.Name(), args, "stack_name", true)
	if err != nil {
		return nil, err
	}
	if _, err := tools.ArgString(t.Name(), args, "reason", true); err != nil {
		return nil, err
	}
	usePrev := false
	if v, ok := args["use_previous_template"].(bool); ok {
		usePrev = v
	}
	body, err := tools.ArgString(t.Name(), args, "template_body", false)
	if err != nil {
		return nil, err
	}
	url, err := tools.ArgString(t.Name(), args, "template_url", false)
	if err != nil {
		return nil, err
	}
	if !usePrev && body == "" && url == "" {
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBadArgs, Tool: t.Name(),
			Message: "exactly one of use_previous_template, template_body, template_url must be set",
		}
	}
	paramsMap, err := tools.ArgMap(t.Name(), args, "parameters", false)
	if err != nil {
		return nil, err
	}
	parameters, err := parametersFromArgs(t.Name(), paramsMap)
	if err != nil {
		return nil, err
	}
	caps, err := tools.ArgStringSlice(t.Name(), args, "capabilities", false)
	if err != nil {
		return nil, err
	}
	in := &cloudformation.UpdateStackInput{
		StackName:    aws.String(name),
		Parameters:   parameters,
		Capabilities: capabilitiesFromArgs(caps),
	}
	if usePrev {
		in.UsePreviousTemplate = aws.Bool(true)
	}
	if body != "" {
		in.TemplateBody = aws.String(body)
	}
	if url != "" {
		in.TemplateURL = aws.String(url)
	}
	api, err := cfnClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	out, err := api.UpdateStack(ctx, in)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"stack_id": aws.ToString(out.StackId),
	}, nil
}

// createStack issues cloudformation:CreateStack.
type createStack struct{}

// Name reports the catalogue name.
func (createStack) Name() string { return "cfn/create-stack" }

// Permission returns the const PermissionWrite.
func (createStack) Permission() tools.Permission { return tools.PermissionWrite }

// Schema declares the args.
func (t createStack) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Create a new CloudFormation stack. Pass either template_body or template_url.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"stack_name":    map[string]any{"type": "string", "description": "New stack name."},
				"template_body": map[string]any{"type": "string", "description": "Inline template."},
				"template_url":  map[string]any{"type": "string", "description": "S3 URL to a template."},
				"parameters":    map[string]any{"type": "object", "description": "Map of parameter key -> value."},
				"capabilities":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "IAM/NAMED_IAM/AUTO_EXPAND as required."},
				"reason":        map[string]any{"type": "string", "description": "Why this stack is being created."},
			},
			"required": []string{"stack_name", "reason"},
		},
	}
}

// Execute issues CreateStack.
func (t createStack) Execute(ctx context.Context, args map[string]any) (any, error) {
	name, err := tools.ArgString(t.Name(), args, "stack_name", true)
	if err != nil {
		return nil, err
	}
	if _, err := tools.ArgString(t.Name(), args, "reason", true); err != nil {
		return nil, err
	}
	body, err := tools.ArgString(t.Name(), args, "template_body", false)
	if err != nil {
		return nil, err
	}
	url, err := tools.ArgString(t.Name(), args, "template_url", false)
	if err != nil {
		return nil, err
	}
	if body == "" && url == "" {
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBadArgs, Tool: t.Name(),
			Message: "one of template_body or template_url is required",
		}
	}
	paramsMap, err := tools.ArgMap(t.Name(), args, "parameters", false)
	if err != nil {
		return nil, err
	}
	parameters, err := parametersFromArgs(t.Name(), paramsMap)
	if err != nil {
		return nil, err
	}
	caps, err := tools.ArgStringSlice(t.Name(), args, "capabilities", false)
	if err != nil {
		return nil, err
	}
	in := &cloudformation.CreateStackInput{
		StackName:    aws.String(name),
		Parameters:   parameters,
		Capabilities: capabilitiesFromArgs(caps),
	}
	if body != "" {
		in.TemplateBody = aws.String(body)
	}
	if url != "" {
		in.TemplateURL = aws.String(url)
	}
	api, err := cfnClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	out, err := api.CreateStack(ctx, in)
	if err != nil {
		return nil, err
	}
	return map[string]any{"stack_id": aws.ToString(out.StackId)}, nil
}

// deleteStack issues cloudformation:DeleteStack.
type deleteStack struct{}

// Name reports the catalogue name.
func (deleteStack) Name() string { return "cfn/delete-stack" }

// Permission returns the const PermissionWrite.
func (deleteStack) Permission() tools.Permission { return tools.PermissionWrite }

// Schema declares the args.
func (t deleteStack) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Delete a CloudFormation stack.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"stack_name": map[string]any{"type": "string", "description": "Stack name or ARN."},
				"reason":     map[string]any{"type": "string", "description": "Why the stack is being deleted."},
			},
			"required": []string{"stack_name", "reason"},
		},
	}
}

// Execute issues DeleteStack.
func (t deleteStack) Execute(ctx context.Context, args map[string]any) (any, error) {
	name, err := tools.ArgString(t.Name(), args, "stack_name", true)
	if err != nil {
		return nil, err
	}
	if _, err := tools.ArgString(t.Name(), args, "reason", true); err != nil {
		return nil, err
	}
	api, err := cfnClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	if _, err := api.DeleteStack(ctx, &cloudformation.DeleteStackInput{
		StackName: aws.String(name),
	}); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true}, nil
}

// cancelUpdateStack issues cloudformation:CancelUpdateStack.
type cancelUpdateStack struct{}

// Name reports the catalogue name.
func (cancelUpdateStack) Name() string { return "cfn/cancel-update-stack" }

// Permission returns the const PermissionWrite.
func (cancelUpdateStack) Permission() tools.Permission { return tools.PermissionWrite }

// Schema declares the args.
func (t cancelUpdateStack) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Cancel an in-flight CloudFormation stack update.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"stack_name": map[string]any{"type": "string", "description": "Stack name or ARN."},
				"reason":     map[string]any{"type": "string", "description": "Why the update is being cancelled."},
			},
			"required": []string{"stack_name", "reason"},
		},
	}
}

// Execute issues CancelUpdateStack.
func (t cancelUpdateStack) Execute(ctx context.Context, args map[string]any) (any, error) {
	name, err := tools.ArgString(t.Name(), args, "stack_name", true)
	if err != nil {
		return nil, err
	}
	if _, err := tools.ArgString(t.Name(), args, "reason", true); err != nil {
		return nil, err
	}
	api, err := cfnClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	if _, err := api.CancelUpdateStack(ctx, &cloudformation.CancelUpdateStackInput{
		StackName: aws.String(name),
	}); err != nil {
		return nil, err
	}
	return map[string]any{"cancelled": true}, nil
}

func init() {
	tools.MustRegister(tools.Default, updateStack{})
	tools.MustRegister(tools.Default, createStack{})
	tools.MustRegister(tools.Default, deleteStack{})
	tools.MustRegister(tools.Default, cancelUpdateStack{})
}
