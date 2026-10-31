// Package leader elects one leading replica through a Kubernetes lease (coordination.k8s.io/v1).
package leader

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// Default timings of the election, used where Config leaves them zero.
const (
	DefaultLeaseDuration = 15 * time.Second
	DefaultRenewDeadline = 10 * time.Second
	DefaultRetryPeriod   = 2 * time.Second
)

// LeaseName is the name of the lease bouncer's replicas compete for.
const LeaseName = "bouncer"

// namespaceFile holds the pod's namespace in the service-account mount.
const namespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// Config names the lease and this replica.
type Config struct {
	Namespace string // POD_NAMESPACE, or the namespace file of the service-account mount
	Name      string // lease name, "bouncer"
	Identity  string // POD_NAME, or the hostname
	// Timings; zero means the defaults 15s / 10s / 2s (lease duration, renew deadline, retry period).
	LeaseDuration, RenewDeadline, RetryPeriod time.Duration
}

// FromEnvironment builds the Config of this pod: POD_NAMESPACE or /var/run/secrets/kubernetes.io/serviceaccount/namespace,
// POD_NAME or os.Hostname, lease "bouncer".
func FromEnvironment() (Config, error) {
	return fromEnvironment(namespaceFile)
}

func fromEnvironment(nsFile string) (Config, error) {
	cfg := Config{Name: LeaseName}

	cfg.Namespace = os.Getenv("POD_NAMESPACE")
	if cfg.Namespace == "" {
		b, err := os.ReadFile(nsFile)
		if err != nil {
			return Config{}, fmt.Errorf("determining the namespace: POD_NAMESPACE is unset and %w", err)
		}
		cfg.Namespace = strings.TrimSpace(string(b))
		if cfg.Namespace == "" {
			return Config{}, fmt.Errorf("determining the namespace: POD_NAMESPACE is unset and %s is empty", nsFile)
		}
	}

	cfg.Identity = os.Getenv("POD_NAME")
	if cfg.Identity == "" {
		host, err := os.Hostname()
		if err != nil {
			return Config{}, fmt.Errorf("determining the identity: POD_NAME is unset and %w", err)
		}
		cfg.Identity = host
	}
	return cfg, nil
}

// InClusterClient returns a clientset from the in-cluster configuration.
func InClusterClient() (kubernetes.Interface, error) {
	rc, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("loading the in-cluster configuration: %w", err)
	}
	client, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("creating the kubernetes client: %w", err)
	}
	return client, nil
}

// ErrLost is returned by Run when this replica held the lease and lost it.
var ErrLost = errors.New("leadership lost")

// Run takes part in the election until ctx is cancelled. It blocks while another replica leads. Once this replica
// becomes leader it calls lead with a context that is cancelled when the lease is lost; it returns lead's result when
// lead returns, ErrLost (after lead has returned) when the lease was lost, and ctx.Err() when ctx ended first. The
// lease is released on cancellation so a successor takes over without waiting for it to expire.
func Run(ctx context.Context, client kubernetes.Interface, cfg Config, lead func(ctx context.Context) error) error {
	if cfg.LeaseDuration == 0 {
		cfg.LeaseDuration = DefaultLeaseDuration
	}
	if cfg.RenewDeadline == 0 {
		cfg.RenewDeadline = DefaultRenewDeadline
	}
	if cfg.RetryPeriod == 0 {
		cfg.RetryPeriod = DefaultRetryPeriod
	}
	l := slog.With("lease", cfg.Namespace+"/"+cfg.Name, "identity", cfg.Identity)

	// The elector keeps renewing until its context ends, also after lead returned; electorCtx lets us stop it then.
	electorCtx, stopElector := context.WithCancel(ctx)
	defer stopElector()

	// OnStartedLeading runs in a goroutine of its own that may not have been scheduled yet when the elector returns.
	// started and closed, guarded by mu, decide race-free whether lead runs and therefore whether to wait for it.
	var (
		mu       sync.Mutex
		started  bool
		closed   bool
		leadDone = make(chan struct{})
		leadErr  error
		// leadOwn records that lead returned while its context was still live, i.e. on its own.
		leadOwn bool
	)

	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock: &resourcelock.LeaseLock{
			LeaseMeta:  metav1.ObjectMeta{Namespace: cfg.Namespace, Name: cfg.Name},
			Client:     client.CoordinationV1(),
			LockConfig: resourcelock.ResourceLockConfig{Identity: cfg.Identity},
		},
		LeaseDuration:   cfg.LeaseDuration,
		RenewDeadline:   cfg.RenewDeadline,
		RetryPeriod:     cfg.RetryPeriod,
		ReleaseOnCancel: true,
		Name:            cfg.Name,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(leadCtx context.Context) {
				mu.Lock()
				if closed {
					mu.Unlock()
					return
				}
				started = true
				mu.Unlock()

				defer close(leadDone)
				l.Info("Started leading")
				leadErr = lead(leadCtx)
				leadOwn = leadCtx.Err() == nil
				// Stop renewing so that the lease is released when lead is done
				stopElector()
			},
			OnStoppedLeading: func() {
				l.Info("Stopped leading")
			},
			OnNewLeader: func(identity string) {
				l.Info("New leader observed", "leader", identity)
			},
		},
	})
	if err != nil {
		return fmt.Errorf("configuring leader election: %w", err)
	}

	l.Info("Joining leader election")
	// Returns once electorCtx ends (releasing the lease if held) or after the lease was lost
	elector.Run(electorCtx)

	mu.Lock()
	closed = true
	wasStarted := started
	mu.Unlock()
	if wasStarted {
		<-leadDone
		if leadOwn {
			return leadErr
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// The elector returns with electorCtx live only after it held the lease and failed to renew it.
	return ErrLost
}
