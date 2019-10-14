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
This requires golang >= 1.11 and optionally a debian machine/container, in case you
want to build the package. You'll also need [go-bindata](https://github.com/kevinburke/go-bindata)
in your path, just do
```
go get -u github.com/kevinburke/go-bindata/...
```
in case you have `$GOPATH/bin` in your path.
You'll then need to generate sources using
```
go generate github.com/VSETH-GECO/bouncer/...
```

For the debian package:
```
dpkg-buildpackage -b -us -uc
```

Binary only:
```
go build -o bouncer github.com/VSETH-GECO/bouncer/cmd
```
