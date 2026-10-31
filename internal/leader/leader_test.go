package leader

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const testNamespace = "bouncer"

// newClient returns a fake clientset whose leases have the API server's optimistic concurrency on resourceVersion.
// The fake tracker ignores resourceVersion and accepts every update; this reactor gives each write a new
// resourceVersion and rejects an update carrying a stale one with a conflict, the way the real API server does.
// TestLostLease depends on it: the elector renews optimistically with its cached lease, and without the conflict that
// renewal would silently overwrite the lease another identity took over, so the leader would never lose it. Do not
// replace it with a plain fake.NewClientset().
func newClient() *fake.Clientset {
	client := fake.NewClientset()
	var (
		mu      sync.Mutex
		version int
	)
	client.PrependReactor("*", "leases", func(action k8stesting.Action) (bool, runtime.Object, error) {
		var lease *coordinationv1.Lease
		switch a := action.(type) {
		case k8stesting.CreateAction:
			lease = a.GetObject().(*coordinationv1.Lease)
		case k8stesting.UpdateAction:
			lease = a.GetObject().(*coordinationv1.Lease)
		default:
			return false, nil, nil
		}
		mu.Lock()
		defer mu.Unlock()
		if action.GetVerb() == "update" {
			current, err := client.Tracker().Get(action.GetResource(), action.GetNamespace(), lease.Name)
			if err != nil {
				return true, nil, err
			}
			if current.(*coordinationv1.Lease).ResourceVersion != lease.ResourceVersion {
				return true, nil, apierrors.NewConflict(action.GetResource().GroupResource(), lease.Name,
					errors.New("the object has been modified"))
			}
		}
		version++
		lease = lease.DeepCopy()
		lease.ResourceVersion = strconv.Itoa(version)
		var err error
		if action.GetVerb() == "update" {
			err = client.Tracker().Update(action.GetResource(), lease, action.GetNamespace())
		} else {
			err = client.Tracker().Create(action.GetResource(), lease, action.GetNamespace())
		}
		if err != nil {
			return true, nil, err
		}
		return true, lease.DeepCopy(), nil
	})
	return client
}

func testConfig(identity string) Config {
	return Config{
		Namespace:     testNamespace,
		Name:          LeaseName,
		Identity:      identity,
		LeaseDuration: 2 * time.Second,
		RenewDeadline: 1 * time.Second,
		RetryPeriod:   200 * time.Millisecond,
	}
}

// replica is one Run under test.
type replica struct {
	cancel  context.CancelFunc
	started chan context.Context // receives lead's context when lead starts
	result  chan error           // receives Run's result
	done    chan struct{}        // closed when Run returned
}

// startReplica runs Run in the background with a lead that blocks until its context ends and then returns leadErr
// (or, when leadErr is nil, the context's error). The replica is cancelled on test cleanup.
func startReplica(t *testing.T, client kubernetes.Interface, identity string, lead func(ctx context.Context) error) *replica {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &replica{cancel: cancel, started: make(chan context.Context, 1), result: make(chan error, 1), done: make(chan struct{})}
	if lead == nil {
		lead = func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}
	}
	go func() {
		defer close(r.done)
		r.result <- Run(ctx, client, testConfig(identity), func(ctx context.Context) error {
			r.started <- ctx
			return lead(ctx)
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(5 * time.Second):
			t.Errorf("Run of %s didn't return after cancellation", identity)
		}
	})
	return r
}

// awaitLeader waits for exactly one of the replicas to start leading and returns its index and lead context.
func awaitLeader(t *testing.T, within time.Duration, rs ...*replica) (int, context.Context) {
	t.Helper()
	timeout := time.After(within)
	for {
		for i, r := range rs {
			select {
			case ctx := <-r.started:
				return i, ctx
			default:
			}
		}
		select {
		case <-timeout:
			t.Fatalf("no replica started leading within %s", within)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func assertNotStarted(t *testing.T, r *replica, name string) {
	t.Helper()
	select {
	case <-r.started:
		t.Fatalf("replica %s started leading as well", name)
	default:
	}
}

func awaitResult(t *testing.T, r *replica, within time.Duration) error {
	t.Helper()
	select {
	case err := <-r.result:
		return err
	case <-time.After(within):
		t.Fatalf("Run didn't return within %s", within)
		return nil
	}
}

func holder(t *testing.T, client kubernetes.Interface) string {
	t.Helper()
	lease, err := client.CoordinationV1().Leases(testNamespace).Get(context.Background(), LeaseName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting lease: %v", err)
	}
	if lease.Spec.HolderIdentity == nil {
		return ""
	}
	return *lease.Spec.HolderIdentity
}

func TestOneLeaderAndHandoverOnCancel(t *testing.T) {
	client := newClient()
	a := startReplica(t, client, "a", nil)
	b := startReplica(t, client, "b", nil)
	names := []string{"a", "b"}
	rs := []*replica{a, b}

	start := time.Now()
	i, leadCtx := awaitLeader(t, 3*time.Second, a, b)
	t.Logf("%s leads after %s", names[i], time.Since(start).Round(time.Millisecond))
	leaderR, follower := rs[i], rs[1-i]

	// The follower stays blocked while the leader holds the lease
	time.Sleep(2500 * time.Millisecond)
	assertNotStarted(t, follower, names[1-i])
	if leadCtx.Err() != nil {
		t.Fatalf("leader's lead context ended: %v", leadCtx.Err())
	}

	start = time.Now()
	leaderR.cancel()
	if err := awaitResult(t, leaderR, 3*time.Second); !errors.Is(err, context.Canceled) || errors.Is(err, ErrLost) {
		t.Fatalf("Run of the cancelled leader returned %v, want context.Canceled", err)
	}
	// The lease is released on cancellation, so the follower takes over well within the lease duration
	select {
	case <-follower.started:
		t.Logf("%s took over %s after the cancellation", names[1-i], time.Since(start).Round(time.Millisecond))
	case <-time.After(2 * time.Second):
		t.Fatalf("follower %s didn't take over within the lease duration", names[1-i])
	}
}

func TestLeadErrorReleasesLease(t *testing.T) {
	client := newClient()
	boom := errors.New("boom")
	a := startReplica(t, client, "a", func(ctx context.Context) error {
		select {
		case <-time.After(300 * time.Millisecond):
			return boom
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	awaitLeader(t, 3*time.Second, a)
	if err := awaitResult(t, a, 3*time.Second); !errors.Is(err, boom) {
		t.Fatalf("Run returned %v, want %v", err, boom)
	}
	if h := holder(t, client); h != "" {
		t.Fatalf("lease still held by %q after lead failed", h)
	}

	// A successor acquires right away instead of waiting for the lease to expire
	start := time.Now()
	b := startReplica(t, client, "b", nil)
	awaitLeader(t, 1*time.Second, b)
	t.Logf("b leads %s after a's failure", time.Since(start).Round(time.Millisecond))
}

func TestLostLease(t *testing.T) {
	client := newClient()
	a := startReplica(t, client, "a", nil)
	_, leadCtx := awaitLeader(t, 3*time.Second, a)
	b := startReplica(t, client, "b", nil)

	// Someone else takes the lease over behind a's back
	ctx := context.Background()
	lease, err := client.CoordinationV1().Leases(testNamespace).Get(ctx, LeaseName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting lease: %v", err)
	}
	c := "c"
	now := metav1.NewMicroTime(time.Now())
	lease.Spec = coordinationv1.LeaseSpec{
		HolderIdentity:       &c,
		LeaseDurationSeconds: lease.Spec.LeaseDurationSeconds,
		AcquireTime:          &now,
		RenewTime:            &now,
	}
	if _, err := client.CoordinationV1().Leases(testNamespace).Update(ctx, lease, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("updating lease: %v", err)
	}
	start := time.Now()

	select {
	case <-leadCtx.Done():
		t.Logf("a's lead context cancelled %s after the takeover", time.Since(start).Round(time.Millisecond))
	case <-time.After(3 * time.Second):
		t.Fatalf("a's lead context wasn't cancelled after the lease was taken over (holder %q)", holder(t, client))
	}
	if err := awaitResult(t, a, 2*time.Second); !errors.Is(err, ErrLost) {
		t.Fatalf("Run returned %v, want ErrLost", err)
	}
	assertNotStarted(t, b, "b")
}

func TestFromEnvironment(t *testing.T) {
	dir := t.TempDir()
	nsFile := filepath.Join(dir, "namespace")
	if err := os.WriteFile(nsFile, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("environment", func(t *testing.T) {
		t.Setenv("POD_NAMESPACE", "ns")
		t.Setenv("POD_NAME", "bouncer-0")
		cfg, err := fromEnvironment(nsFile)
		if err != nil {
			t.Fatal(err)
		}
		want := Config{Namespace: "ns", Name: "bouncer", Identity: "bouncer-0"}
		if cfg != want {
			t.Fatalf("got %+v, want %+v", cfg, want)
		}
	})
	t.Run("fallbacks", func(t *testing.T) {
		t.Setenv("POD_NAMESPACE", "")
		t.Setenv("POD_NAME", "")
		cfg, err := fromEnvironment(nsFile)
		if err != nil {
			t.Fatal(err)
		}
		want := Config{Namespace: "from-file", Name: "bouncer", Identity: host}
		if cfg != want {
			t.Fatalf("got %+v, want %+v", cfg, want)
		}
	})
	t.Run("no namespace", func(t *testing.T) {
		t.Setenv("POD_NAMESPACE", "")
		t.Setenv("POD_NAME", "x")
		if _, err := fromEnvironment(filepath.Join(dir, "missing")); err == nil {
			t.Fatal("expected an error without POD_NAMESPACE and namespace file")
		}
	})
	t.Run("public", func(t *testing.T) {
		t.Setenv("POD_NAMESPACE", "ns")
		t.Setenv("POD_NAME", "p")
		cfg, err := FromEnvironment()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Namespace != "ns" || cfg.Identity != "p" || cfg.Name != "bouncer" {
			t.Fatalf("got %+v", cfg)
		}
	})
}
