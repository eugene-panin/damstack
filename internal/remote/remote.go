// Package remote keeps what the people running a project share on its
// platform's Consul: the lock that lets one of them change it at a time, and
// the OpenTofu state of its steps. Everything is under damstack/<project>/.
package remote

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/consul/api"
)

// KV is the part of Consul's KV store the package uses; *api.KV is one.
type KV interface {
	Get(key string, q *api.QueryOptions) (*api.KVPair, *api.QueryMeta, error)
	List(prefix string, q *api.QueryOptions) (api.KVPairs, *api.QueryMeta, error)
	CAS(p *api.KVPair, q *api.WriteOptions) (bool, *api.WriteMeta, error)
	Acquire(p *api.KVPair, q *api.WriteOptions) (bool, *api.WriteMeta, error)
	Release(p *api.KVPair, q *api.WriteOptions) (bool, *api.WriteMeta, error)
	Delete(key string, w *api.WriteOptions) (*api.WriteMeta, error)
}

// Sessions is the part of Consul's session API the package uses; *api.Session
// is one.
type Sessions interface {
	Create(se *api.SessionEntry, q *api.WriteOptions) (string, *api.WriteMeta, error)
	Renew(id string, q *api.WriteOptions) (*api.SessionEntry, *api.WriteMeta, error)
	Destroy(id string, q *api.WriteOptions) (*api.WriteMeta, error)
	Info(id string, q *api.QueryOptions) (*api.SessionEntry, *api.QueryMeta, error)
}

// Remote is a project's place on its platform's Consul.
type Remote struct {
	KV       KV
	Sessions Sessions
	// Prefix is damstack/<project>, without a trailing slash.
	Prefix string
}

// RequestTimeout bounds each request to Consul, so a tunnel gone mid-request
// does not hang a renewal or a release; it outlasts the longest wait of Lock.
const RequestTimeout = 30 * time.Second

// ErrUnreachable is returned when the platform's Consul does not answer.
var ErrUnreachable = errors.New("the platform's Consul does not answer")

// Open connects to the Consul a platform names in its app_env: CONSUL_HTTP_ADDR,
// CONSUL_CACERT and CONSUL_HTTP_TOKEN. It does not check that it answers.
func Open(project string, env map[string]string) (*Remote, error) {
	client, err := open(env, RequestTimeout)
	if err != nil {
		return nil, err
	}
	return New(project, client.KV(), client.Session()), nil
}

func open(env map[string]string, timeout time.Duration) (*api.Client, error) {
	addr := env["CONSUL_HTTP_ADDR"]
	if addr == "" {
		return nil, errors.New("the platform gives no CONSUL_HTTP_ADDR")
	}
	cfg := api.DefaultNonPooledConfig()
	cfg.Address = addr
	cfg.Token = env["CONSUL_HTTP_TOKEN"]
	cfg.TLSConfig.CAFile = env["CONSUL_CACERT"]
	httpClient, err := api.NewHttpClient(cfg.Transport, cfg.TLSConfig)
	if err != nil {
		return nil, fmt.Errorf("the platform's Consul at %s: %w", addr, err)
	}
	httpClient.Timeout = timeout
	cfg.HttpClient = httpClient
	client, err := api.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("the platform's Consul at %s: %w", addr, err)
	}
	return client, nil
}

// New is a Remote over the given KV store and sessions.
func New(project string, kv KV, sessions Sessions) *Remote {
	return &Remote{KV: kv, Sessions: sessions, Prefix: "damstack/" + project}
}

func (r *Remote) key(parts ...string) string {
	return r.Prefix + "/" + strings.Join(parts, "/")
}

// Reachable is nil when the platform's Consul answers.
func (r *Remote) Reachable() error {
	if _, _, err := r.KV.Get(r.key("lock"), nil); err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	return nil
}
