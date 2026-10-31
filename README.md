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

## Running the tests

The integration tests need Docker. `go test ./...` starts one MySQL 8.0.36 container per run, shared by all test packages and removed by testcontainers' reaper shortly after the run ends. Without Docker the integration tests are skipped; with `CI=1` set that is a failure instead. Tests that insert random rows log their seed: rerun with `TESTGEN_SEED=<seed>` to reproduce them.

## Regenerating query code

The go-jet table and model code under `internal/db/gen` is generated from the schema fixture plus bouncer's migrations. This needs Docker. After changing the schema or migrations, run `go run ./internal/tools/jetgen` from the module root and commit the result. `go test ./internal/jetgen` fails when the committed code is stale.
