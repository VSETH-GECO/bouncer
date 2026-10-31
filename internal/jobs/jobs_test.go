package jobs_test

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/VSETH-GECO/bouncer/internal/db"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/model"
	"github.com/VSETH-GECO/bouncer/internal/db/gen/freeradius/table"
	"github.com/VSETH-GECO/bouncer/internal/jobs"
	"github.com/VSETH-GECO/bouncer/internal/mac"
	"github.com/VSETH-GECO/bouncer/internal/testdb"
	"github.com/VSETH-GECO/bouncer/internal/testgen"
	"github.com/go-jet/jet/v2/mysql"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func ptr[T any](v T) *T { return &v }

func begin(t *testing.T) (context.Context, *sql.Tx) {
	return context.Background(), testdb.Tx(t)
}

func find(list []jobs.Job, id int) (jobs.Job, bool) {
	for _, j := range list {
		if j.ID == id {
			return j, true
		}
	}
	return jobs.Job{}, false
}

// newest returns the highest-id pending job (the one just created in this transaction).
func newest(t *testing.T, ctx context.Context, q *sql.Tx) jobs.Job {
	t.Helper()
	list, err := jobs.Pending(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 {
		t.Fatal("no pending jobs")
	}
	return list[len(list)-1]
}

func TestCreatePendingDelete(t *testing.T) {
	ctx, q := begin(t)
	m := mac.MustParse("00:11:22:33:44:51")

	if err := jobs.Create(ctx, q, m, 42); err != nil {
		t.Fatal(err)
	}
	j := newest(t, ctx, q)
	if j.MAC != m || j.RawMAC != m.Hex() || j.TargetVLAN != 42 || j.Retries != 0 {
		t.Errorf("job = %+v, want MAC %v VLAN 42 retries 0", j, m)
	}

	if err := jobs.Delete(ctx, q, j.ID); err != nil {
		t.Fatal(err)
	}
	list, err := jobs.Pending(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := find(list, j.ID); ok {
		t.Error("job still pending after Delete")
	}
	if err := jobs.Delete(ctx, q, j.ID); err != nil {
		t.Errorf("deleting nonexistent job: %v", err)
	}
}

func TestPending_ExcludesRetired(t *testing.T) {
	ctx, q := begin(t)
	if err := jobs.Create(ctx, q, mac.MustParse("00:11:22:33:44:52"), 10); err != nil {
		t.Fatal(err)
	}
	j := newest(t, ctx, q)

	// Age the row so a CURRENT_TIMESTAMP update is observable.
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := table.BouncerJobs.UPDATE().SET(table.BouncerJobs.LastUpdate.SET(mysql.TimestampT(old))).
		WHERE(table.BouncerJobs.ID.EQ(mysql.Int(int64(j.ID)))).ExecContext(ctx, q)
	if err != nil {
		t.Fatal(err)
	}

	if err := jobs.SetRetries(ctx, q, j.ID, jobs.MaxRetries); err != nil {
		t.Fatal(err)
	}
	list, err := jobs.Pending(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := find(list, j.ID); ok {
		t.Error("job with MaxRetries retries still pending")
	}

	if err := jobs.SetRetries(ctx, q, j.ID, 2); err != nil {
		t.Fatal(err)
	}
	list, err = jobs.Pending(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := find(list, j.ID)
	if !ok {
		t.Fatal("job with 2 retries not pending")
	}
	if got.Retries != 2 {
		t.Errorf("Retries = %d, want 2", got.Retries)
	}
	if !got.LastUpdate.After(old.Add(24 * time.Hour)) {
		t.Errorf("LastUpdate = %v, want it refreshed from %v", got.LastUpdate, old)
	}
}

func TestPending_MalformedMAC(t *testing.T) {
	ctx, q := begin(t)
	if _, err := q.ExecContext(ctx, "INSERT INTO bouncer_jobs(clientMAC, targetVLAN) VALUES('not-a-mac', 7)"); err != nil {
		t.Fatal(err)
	}
	j := newest(t, ctx, q)
	if !j.MAC.IsZero() || j.RawMAC != "not-a-mac" || j.TargetVLAN != 7 {
		t.Errorf("job = %+v, want zero MAC, RawMAC not-a-mac, VLAN 7", j)
	}
}

func lastLog(t *testing.T, ctx context.Context, q *sql.Tx) model.BouncerLog {
	t.Helper()
	var row model.BouncerLog
	err := mysql.SELECT(table.BouncerLog.AllColumns).FROM(table.BouncerLog).
		ORDER_BY(table.BouncerLog.ID.DESC()).LIMIT(1).QueryContext(ctx, q, &row)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestLogResult_Nulls(t *testing.T) {
	ctx, q := begin(t)
	m := mac.MustParse("00:11:22:33:44:53")
	if err := jobs.LogResult(ctx, q, jobs.Result{MAC: m, OldVLAN: -1, NewVLAN: 20}); err != nil {
		t.Fatal(err)
	}
	row := lastLog(t, ctx, q)
	if row.ClientMAC != m.Hex() || row.OldVLAN != -1 || row.NewVLAN != 20 {
		t.Errorf("row = %+v", row)
	}
	if row.SwitchIP != nil || row.SwitchPort != nil || row.ClientName != nil || row.ClientIP != nil {
		t.Errorf("want NULL switchIP/switchPort/clientName/clientIP, got %v %v %v %v",
			row.SwitchIP, row.SwitchPort, row.ClientName, row.ClientIP)
	}
}

func TestLogResult_Values(t *testing.T) {
	ctx, q := begin(t)
	m := mac.MustParse("00:11:22:33:44:54")
	err := jobs.LogResult(ctx, q, jobs.Result{
		MAC: m, OldVLAN: 10, NewVLAN: 20,
		SwitchIP:   net.ParseIP("10.10.0.1"),
		SwitchPort: ptr("Gi1/0/5"),
		Hostname:   ptr("test-pc"),
		ClientIP:   net.ParseIP("10.20.0.50"),
	})
	if err != nil {
		t.Fatal(err)
	}
	row := lastLog(t, ctx, q)
	str := func(p *string) string {
		if p == nil {
			return "<NULL>"
		}
		return *p
	}
	if row.ClientMAC != m.Hex() || row.OldVLAN != 10 || row.NewVLAN != 20 {
		t.Errorf("row = %+v", row)
	}
	if str(row.SwitchIP) != "10.10.0.1" || str(row.SwitchPort) != "Gi1/0/5" ||
		str(row.ClientName) != "test-pc" || str(row.ClientIP) != "10.20.0.50" {
		t.Errorf("got %s %s %s %s", str(row.SwitchIP), str(row.SwitchPort), str(row.ClientName), str(row.ClientIP))
	}
}

func TestCreate_RejectsOutOfRangeVLAN(t *testing.T) {
	ctx, q := begin(t)
	m := mac.MustParse("00:11:22:33:44:56")

	// pending returns the VLANs of the queued jobs for m.
	pending := func() []int {
		list, err := jobs.Pending(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var vlans []int
		for _, j := range list {
			if j.RawMAC == m.Hex() {
				vlans = append(vlans, j.TargetVLAN)
			}
		}
		return vlans
	}

	for _, vlan := range []int{0, -1, 4095, 70000} {
		if err := jobs.Create(ctx, q, m, vlan); err == nil {
			t.Errorf("Create(vlan %d) succeeded, want an error", vlan)
		}
	}
	if got := pending(); len(got) != 0 {
		t.Errorf("rejected VLANs left jobs with VLANs %v, want none", got)
	}

	for _, vlan := range []int{1, 4094} {
		if err := jobs.Create(ctx, q, m, vlan); err != nil {
			t.Fatalf("Create(vlan %d): %v", vlan, err)
		}
	}
	if got := pending(); len(got) != 2 || got[0] != 1 || got[1] != 4094 {
		t.Errorf("pending VLANs = %v, want [1 4094]", got)
	}
}

func TestLatest(t *testing.T) {
	ctx, q := begin(t)
	p := testgen.Persistent(t, q)
	m := p.MAC()

	if _, err := jobs.Latest(ctx, q, m); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("Latest without jobs: err = %v, want db.ErrNotFound", err)
	}

	older := p.Job(func(j *model.BouncerJobs) { j.ClientMAC, j.TargetVLAN = m.Hex(), 10 })
	newer := p.Job(func(j *model.BouncerJobs) {
		j.ClientMAC, j.TargetVLAN, j.Retires = m.Hex(), 20, jobs.MaxRetries
	})
	p.Job() // another client's job, newer still

	got, err := jobs.Latest(ctx, q, m)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != int(newer.ID) || got.ID == int(older.ID) {
		t.Errorf("Latest = job %d, want the newer job %d", got.ID, newer.ID)
	}
	if got.MAC != m || got.RawMAC != m.Hex() || got.TargetVLAN != 20 || got.Retries != jobs.MaxRetries {
		t.Errorf("Latest = %+v, want MAC %v VLAN 20 retries %d (exhausted rows included)", got, m, jobs.MaxRetries)
	}
	if !got.LastUpdate.Equal(newer.LastUpdate) {
		t.Errorf("LastUpdate = %v, want %v", got.LastUpdate, newer.LastUpdate)
	}
}
