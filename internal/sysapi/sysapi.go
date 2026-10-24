// Package sysapi talks to the NATS system account: $SYS.REQ.* requests and advisories (PL-KEY-005, PL-AUTH-007/010).
// Response types are local subsets of the server's JSON so the server package is not linked into pulsed.
package sysapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// Client issues system requests over a SYS account connection.
type Client struct {
	nc      *nats.Conn
	timeout time.Duration

	mu       sync.Mutex
	serverID string
}

// New returns a system API client.
func New(nc *nats.Conn) *Client { return &Client{nc: nc, timeout: 5 * time.Second} }

// APIError is an error returned by the server in a system response.
type APIError struct {
	Code        int    `json:"code"`
	Description string `json:"description"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("nats system api: %d %s", e.Code, e.Description)
}

// ServerInfo identifies the responding server.
type ServerInfo struct {
	Name string    `json:"name"`
	ID   string    `json:"id"`
	Ver  string    `json:"ver"`
	Time time.Time `json:"time"`
}

type response struct {
	Server ServerInfo      `json:"server"`
	Data   json.RawMessage `json:"data"`
	Error  *APIError       `json:"error"`
}

func (c *Client) request(ctx context.Context, subject string, body any, out any) (*ServerInfo, error) {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = b
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	msg, err := c.nc.RequestWithContext(ctx, subject, payload)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", subject, err)
	}
	var r response
	if err := json.Unmarshal(msg.Data, &r); err != nil {
		return nil, fmt.Errorf("%s: decode: %w", subject, err)
	}
	if r.Error != nil {
		return &r.Server, r.Error
	}
	if out != nil && len(r.Data) > 0 {
		if err := json.Unmarshal(r.Data, out); err != nil {
			return &r.Server, fmt.Errorf("%s: decode data: %w", subject, err)
		}
	}
	return &r.Server, nil
}

// ServerID returns (and caches) the ID of the single server.
func (c *Client) ServerID(ctx context.Context) (string, error) {
	c.mu.Lock()
	id := c.serverID
	c.mu.Unlock()
	if id != "" {
		return id, nil
	}
	srv, err := c.request(ctx, "$SYS.REQ.SERVER.PING", nil, nil)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.serverID = srv.ID
	c.mu.Unlock()
	return srv.ID, nil
}

func (c *Client) serverSubject(ctx context.Context, verb string) (string, error) {
	id, err := c.ServerID(ctx)
	if err != nil {
		return "", err
	}
	return "$SYS.REQ.SERVER." + id + "." + verb, nil
}

// Varz is the subset of /varz pulsed uses.
type Varz struct {
	ID             string    `json:"server_id"`
	Name           string    `json:"server_name"`
	Version        string    `json:"version"`
	GoVersion      string    `json:"go"`
	Start          time.Time `json:"start"`
	Now            time.Time `json:"now"`
	Uptime         string    `json:"uptime"`
	Mem            int64     `json:"mem"`
	Cores          int       `json:"cores"`
	CPU            float64   `json:"cpu"`
	Connections    int       `json:"connections"`
	TotalConns     uint64    `json:"total_connections"`
	Subscriptions  uint32    `json:"subscriptions"`
	InMsgs         int64     `json:"in_msgs"`
	OutMsgs        int64     `json:"out_msgs"`
	InBytes        int64     `json:"in_bytes"`
	OutBytes       int64     `json:"out_bytes"`
	SlowConsumers  int64     `json:"slow_consumers"`
	MaxPayload     int       `json:"max_payload"`
	ConfigLoadTime time.Time `json:"config_load_time"`
	TLSRequired    bool      `json:"tls_required"`
}

// Varz returns server variables.
func (c *Client) Varz(ctx context.Context) (*Varz, error) {
	subj, err := c.serverSubject(ctx, "VARZ")
	if err != nil {
		return nil, err
	}
	var v Varz
	_, err = c.request(ctx, subj, nil, &v)
	return &v, err
}

// Jsz is the subset of /jsz pulsed uses.
type Jsz struct {
	Config struct {
		MaxMemory int64  `json:"max_memory"`
		MaxStore  int64  `json:"max_storage"`
		StoreDir  string `json:"store_dir"`
	} `json:"config"`
	Memory    uint64 `json:"memory"`
	Store     uint64 `json:"storage"`
	Streams   int    `json:"streams"`
	Consumers int    `json:"consumers"`
	Messages  uint64 `json:"messages"`
	Bytes     uint64 `json:"bytes"`
	API       struct {
		Total  uint64 `json:"total"`
		Errors uint64 `json:"errors"`
	} `json:"api"`
}

// Jsz returns JetStream usage.
func (c *Client) Jsz(ctx context.Context) (*Jsz, error) {
	subj, err := c.serverSubject(ctx, "JSZ")
	if err != nil {
		return nil, err
	}
	var v Jsz
	_, err = c.request(ctx, subj, nil, &v)
	return &v, err
}

// ConnInfo is the subset of a CONNZ entry pulsed uses.
type ConnInfo struct {
	Cid            uint64    `json:"cid"`
	Kind           string    `json:"kind"`
	Type           string    `json:"type"`
	IP             string    `json:"ip"`
	Port           int       `json:"port"`
	Start          time.Time `json:"start"`
	LastActivity   time.Time `json:"last_activity"`
	RTT            string    `json:"rtt"`
	Uptime         string    `json:"uptime"`
	Pending        int       `json:"pending_bytes"`
	InMsgs         int64     `json:"in_msgs"`
	OutMsgs        int64     `json:"out_msgs"`
	InBytes        int64     `json:"in_bytes"`
	OutBytes       int64     `json:"out_bytes"`
	NumSubs        uint32    `json:"subscriptions"`
	Name           string    `json:"name"`
	Lang           string    `json:"lang"`
	Version        string    `json:"version"`
	TLSVersion     string    `json:"tls_version"`
	AuthorizedUser string    `json:"authorized_user"`
	Account        string    `json:"account"`
	JWT            string    `json:"jwt"`
	IssuerKey      string    `json:"issuer_key"`
	NameTag        string    `json:"name_tag"`
	Tags           []string  `json:"tags"`
}

// Connz is a page of connections.
type Connz struct {
	NumConns int        `json:"num_connections"`
	Total    int        `json:"total"`
	Offset   int        `json:"offset"`
	Limit    int        `json:"limit"`
	Conns    []ConnInfo `json:"connections"`
}

// ConnzOptions filter CONNZ.
type ConnzOptions struct {
	Account string `json:"acc,omitempty"`
	Subs    bool   `json:"subscriptions,omitempty"`
	Auth    bool   `json:"auth,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Offset  int    `json:"offset,omitempty"`
	CID     uint64 `json:"cid,omitempty"`
	User    string `json:"user,omitempty"`
	Sort    string `json:"sort,omitempty"` // e.g. msgs_to, bytes_to, start, last
}

// Connz lists connections.
func (c *Client) Connz(ctx context.Context, o ConnzOptions) (*Connz, error) {
	subj, err := c.serverSubject(ctx, "CONNZ")
	if err != nil {
		return nil, err
	}
	var v Connz
	_, err = c.request(ctx, subj, o, &v)
	return &v, err
}

// ClaimsLookup returns the account JWT the resolver currently holds, or "" when it has none.
func (c *Client) ClaimsLookup(ctx context.Context, accountPub string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	msg, err := c.nc.RequestWithContext(ctx, "$SYS.REQ.ACCOUNT."+accountPub+".CLAIMS.LOOKUP", nil)
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrNoResponders) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(msg.Data), nil
}

// ClaimsUpdate pushes an account JWT to the full resolver.
func (c *Client) ClaimsUpdate(ctx context.Context, accountJWT string) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	msg, err := c.nc.RequestWithContext(ctx, "$SYS.REQ.CLAIMS.UPDATE", []byte(accountJWT))
	if err != nil {
		return fmt.Errorf("claims update: %w", err)
	}
	var r struct {
		Data *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"data"`
		Error *APIError `json:"error"`
	}
	if err := json.Unmarshal(msg.Data, &r); err != nil {
		return fmt.Errorf("claims update: decode: %w", err)
	}
	if r.Error != nil {
		return r.Error
	}
	if r.Data == nil || r.Data.Code != 200 {
		return fmt.Errorf("claims update: unexpected response %s", msg.Data)
	}
	return nil
}

// Reload asks the server to reload its configuration (and TLS certificates).
func (c *Client) Reload(ctx context.Context) error {
	subj, err := c.serverSubject(ctx, "RELOAD")
	if err != nil {
		return err
	}
	_, err = c.request(ctx, subj, nil, nil)
	return err
}

// Kick disconnects a client by connection ID.
func (c *Client) Kick(ctx context.Context, cid uint64) error {
	subj, err := c.serverSubject(ctx, "KICK")
	if err != nil {
		return err
	}
	_, err = c.request(ctx, subj, map[string]uint64{"cid": cid}, nil)
	return err
}

// ClientInfo is the client section of connect/disconnect/auth-error advisories.
type ClientInfo struct {
	Start   time.Time `json:"start"`
	Host    string    `json:"host"`
	ID      uint64    `json:"id"`
	Account string    `json:"acc"`
	User    string    `json:"user"`
	Name    string    `json:"name"`
	Lang    string    `json:"lang"`
	Version string    `json:"ver"`
	Kind    string    `json:"kind"`
	Type    string    `json:"client_type"`
	NameTag string    `json:"name_tag"`
	Tags    []string  `json:"tags"`
	Issuer  string    `json:"issuer_key"`
}

// Advisory is a connect, disconnect or auth error event.
type Advisory struct {
	Type   string     `json:"type"`
	ID     string     `json:"id"`
	Time   time.Time  `json:"timestamp"`
	Server ServerInfo `json:"server"`
	Client ClientInfo `json:"client"`
	Reason string     `json:"reason"`
}

// Advisory subjects.
const (
	SubjAuthErr    = "$SYS.SERVER.*.CLIENT.AUTH.ERR"
	SubjConnect    = "$SYS.ACCOUNT.*.CONNECT"
	SubjDisconnect = "$SYS.ACCOUNT.*.DISCONNECT"
)

// Subscribe delivers advisories on subject to fn. Malformed advisories are dropped.
func (c *Client) Subscribe(subject string, fn func(Advisory)) (*nats.Subscription, error) {
	return c.nc.Subscribe(subject, func(m *nats.Msg) {
		var a Advisory
		if json.Unmarshal(m.Data, &a) == nil {
			fn(a)
		}
	})
}
