# reference-pack

The reference Packwright pack. Ships with the Packwright repo and serves
as the canonical example of what a pack looks like — community packs are
expected to follow the same layout.

The pack is intentionally thin: every manifest in `manifests/` references
a CloudFormation template that lives *outside* the pack, in a sibling
directory of the repo (e.g. `alb-template/alb-template.yaml`). The pack
provides the *form schema*, the *deploy driver*, and the *fixtures*; the
CFN YAML stays where it always has.

## Contents

```
pack.yaml          Pack metadata (name, version, requires)
manifests/         One YAML file per slash command. The reference pack
                   currently ships `/alb`.
templates/         Intentionally empty — see templates/README.md.
deploy.sh          Generic CFN deploy wrapper. Reusable across all
                   resource manifests in this pack.
tests/             Fixtures + a manifest_test.sh that exercises deploy.sh
                   against a golden parameters.json.
```

## Installing locally

Once `packwright packs add` lands (MVP-4 PR-01), packs install into
`$HOME/.config/packwright/packs/<name>/` via `git clone`. Until then,
point Packwright at this pack with:

```sh
export PACKWRIGHT_HOME=$(pwd)/packs   # one level above the pack dir
packwright tui                        # /alb appears in the palette
```

## What `/alb` does

Provisions or updates an Application Load Balancer stack via the
existing `alb-template/alb-template.yaml` CloudFormation template. The
manifest exercises every meaningful capability of the resource engine:
live AWS pickers (VPC, subnets, security groups, ACM cert), a
cross-field dependency (`SubnetIds` filters by chosen `VpcId`), a
custom validator (`distinct-az` requires subnets across ≥2 AZs), the
script deploy driver, and the live CloudFormation event stream.

See `feature/mvp1/adrs/0012-first-action-alb.md` for the design
rationale.

## Running the tests

```sh
bash packs/reference/tests/manifest_test.sh
```

The test does not call AWS — `deploy.sh` is invoked with `DRY_RUN=1`
and the output diffed against the golden file.

## License

Apache-2.0 (inherits from the repo). See `LICENSE` at the repo root.
