# bouncer

This software can be used to bounce ports on switches.

## Rationale

When deploying FreeRADIUS and kea with our roles and pointing switches to them, you
can already dynamically assign users to VLANs using their MAC. However, moving them
between VLANs will still require manual intervention as changing the RADIUS entries
in the database will only apply on the next login. If only there was a way to force
switches to reauth...

It turns out there is: RADIUS Change-of-Authorization, aka RADIUS CoA.

FreeRADIUS can _consume_ this or send it via the `radclient` command line which is
a little bit cumbersome to script. That's where this software comes in: You point
it to the RADIUS database, give it the CoA secret you set on the switches and it
will do everything for you. Specifically, there will be a table called `bouncer_jobs`.
Insert a client's MAC and it's target VLAN into this table and bouncer will

* Find the client's RADIUS settings and update them to the new VLAN
* Figure out the current running RADIUS session it belongs to
* Send a CoA to the respective switch

At the moment, we use Cisco's proprietary `AVPair` command, so the switch port will
_physically_ toggle, therefore the client will also fetch a new DHCP lease if needed!

## Build

This requires Go 1.25.

```bash
go build -o bouncer ./cmd/bouncer
```

The container image is built from the `Dockerfile`.

## Deployment

bouncer runs with two replicas in Kubernetes. They elect a leader through the lease `bouncer` (`coordination.k8s.io`) in the pod's namespace, using the in-cluster configuration, `POD_NAMESPACE` and `POD_NAME`; the service account needs get/list/watch/create/update/patch on leases. Only the leader processes jobs and runs the Discord bot; when it loses the lease it exits and is restarted as a follower, and on shutdown it releases the lease so the other replica takes over within seconds. Every replica serves `/metrics` (`bouncer_leader` is 1 on the leader). To run a single instance locally, outside Kubernetes, pass `--leader-elect=false`.

Every replica answers the probes on the metrics port (`prometheusPort`, default 2112), which is bound even with `--usePrometheus=false` (that only drops `/metrics` and its sampler). `/healthz` is the liveness probe: it is 200 while every loop (the metrics sampler, and on the leader the job loop and the reconciliation loop) keeps completing rounds successfully, and 503 once one of them has not succeeded for 60 s (5 min for the reconciliation loop); a loop whose rounds keep failing backs off (doubling up to half that window) and so ends in a restart. `/readyz` is the readiness probe: liveness, plus every loop having succeeded at least once since it started, plus a database ping with a 2 s timeout. Each loop exports `bouncer_loop_*` series labelled by its name. `/metrics` serves the Prometheus metrics.

The leader also runs the reconciliation loop every 30 s. It looks for clients stuck after moving to another switch without logging out: an open RADIUS session on a switch of the switch map whose event VLANs do not include the client's assigned VLAN. It skips clients in the login VLAN (499), switches missing from the switch map and switches without event VLANs, and only looks at a client's newest open session. For each stuck client it queues a job moving it to the login VLAN, unless the client already has a pending job or its last job was updated less than 10 minutes ago; the job loop then disconnects the client as for a logout. It logs one line when a client becomes stuck and one when it no longer is, and exports `bouncer_stuck_clients` and `bouncer_stuck_bounces_total`. By default it runs dry (`--reconcile-dry-run`, config key `reconcile-dry-run`, default true): it only logs and counts the stuck clients and queues no job. Pass `--reconcile-dry-run=false` to let it queue the jobs.

## Running the tests

The integration tests need Docker. `go test ./...` starts one MySQL 8.0.36 container per run, shared by all test packages and removed by testcontainers' reaper shortly after the run ends. Without Docker the integration tests are skipped; with `CI=1` set that is a failure instead. Tests that insert random rows log their seed: rerun with `TESTGEN_SEED=<seed>` to reproduce them.

## Regenerating query code

The go-jet table and model code under `internal/db/gen` is generated from the schema fixture plus bouncer's migrations. This needs Docker. After changing the schema or migrations, run `go run ./internal/tools/jetgen` from the module root and commit the result. `go test ./internal/jetgen` fails when the committed code is stale.
