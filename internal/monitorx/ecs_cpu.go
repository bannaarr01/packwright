package monitorx

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

// ecsCPU implements the "ecs/cpu" panel kind. It is a thin composition over
// cloudwatch/metric: the YAML caller supplies cluster + service + lookback,
// and we expand that into a CW metric query against AWS/ECS / CPUUtilization
// keyed on (ClusterName, ServiceName). The composition lives in this file
// rather than at the runner level so adding a sibling ecs/memory panel later
// is a single new file, not a parser change.
//
//	kind: ecs/cpu
//	spec:
//	  cluster: prod
//	  service: api
//	  lookback: 1h
//	  period: 60                          # optional; CloudWatch minimum is 60s
//	  statistic: Average                  # optional; defaults to Average
type ecsCPU struct {
	inner cwMetric
}

func init() {
	Register("ecs/cpu", func() Panel { return &ecsCPU{} })
}

// Kind reports the registered panel kind.
func (e *ecsCPU) Kind() string { return "ecs/cpu" }

// Validate translates the ecs/cpu spec into the equivalent cloudwatch/metric
// spec and delegates to cwMetric.Validate. Missing lookback / period surface
// from the inner validator with the same diagnostic message authors get for
// a raw cloudwatch/metric panel.
func (e *ecsCPU) Validate(spec map[string]any) error {
	cluster, err := requireString(spec, "cluster")
	if err != nil {
		return err
	}
	service, err := requireString(spec, "service")
	if err != nil {
		return err
	}

	stat, _ := spec["statistic"].(string)
	if stat == "" {
		stat = "Average"
	}

	cwSpec := map[string]any{
		"namespace": "AWS/ECS",
		"metric":    "CPUUtilization",
		"statistic": stat,
		"unit":      "Percent",
		"dimensions": map[string]any{
			"ClusterName": cluster,
			"ServiceName": service,
		},
	}
	if v, ok := spec["lookback"]; ok {
		cwSpec["lookback"] = v
	}
	if v, ok := spec["period"]; ok {
		cwSpec["period"] = v
	}
	return e.inner.Validate(cwSpec)
}

// Refresh delegates to the inner cwMetric and relabels the series so the
// chart legend reads "<service> CPU" rather than the verbose dimension
// concatenation cwMetric would otherwise pick.
func (e *ecsCPU) Refresh(ctx context.Context, deps Deps) (PanelData, error) {
	data, err := e.inner.Refresh(ctx, deps)
	if err != nil {
		return nil, err
	}
	sd, ok := data.(SeriesData)
	if !ok || len(sd.Series) == 0 {
		return data, nil
	}
	// cwMetric stored the dimensions on inner; the second one (sorted by
	// dimension name) is ServiceName.
	service := serviceFromDims(e.inner.dimensions)
	if service != "" {
		sd.Series[0].Label = service + " CPU"
	}
	return sd, nil
}

func serviceFromDims(dims []cwtypes.Dimension) string {
	for _, d := range dims {
		if aws.ToString(d.Name) == "ServiceName" {
			return aws.ToString(d.Value)
		}
	}
	return ""
}
