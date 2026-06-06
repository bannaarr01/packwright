# `templates/` is intentionally empty

The reference pack does not vendor CFN YAML. Each manifest in
`manifests/` points its `template.path` at the canonical template
directories at the repo root (e.g. `../../alb-template/alb-template.yaml`),
preserving a single source of truth.
