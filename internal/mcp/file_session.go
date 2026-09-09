package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/tools"
)

// FileSession owns MCP clients for one agent session. A client is shared by
// the tools discovered from one file declaration and one trusted credential
// owner. There is deliberately no process-wide pool: the session is the
// lifetime boundary for remote handles.
type FileSession struct {
	svc    *Service
	ctx    context.Context
	cancel context.CancelFunc

	mu          sync.Mutex
	connections map[string]*fileConnection
	closed      bool
	connect     fileSessionConnector
}

type fileSessionConnector func(context.Context, Registration, CredentialOwner, func()) (RemoteClient, error)

// fileConnection is a borrowed session-owned handle. toolProxy never closes
// it; only FileSession does.
type fileConnection struct {
	mu      sync.Mutex
	client  RemoteClient
	key     string
	grant   string
	logical string
	dirty   bool
	closed  bool
	cancel  context.CancelFunc
}

// fileConnectionIndex is a live-handle index used only by explicit file
// disconnect. It carries no authorization state and is never a pool.
type fileConnectionIndex struct {
	mu     sync.Mutex
	grants map[string]map[*fileConnection]struct{}
}

func (i *fileConnectionIndex) add(reg Registration, owner CredentialOwner, conn *fileConnection) {
	if i == nil || conn == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.grants == nil {
		i.grants = make(map[string]map[*fileConnection]struct{})
	}
	key := fileCredentialKey(reg, owner)
	if i.grants[key] == nil {
		i.grants[key] = make(map[*fileConnection]struct{})
	}
	i.grants[key][conn] = struct{}{}
}

func (i *fileConnectionIndex) removeKey(key string, conn *fileConnection) {
	if i == nil || conn == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	connections := i.grants[key]
	if connections == nil {
		return
	}
	delete(connections, conn)
	if len(connections) == 0 {
		delete(i.grants, key)
	}
}

// closeGrant snapshots and removes matching handles before SDK cleanup, so a
// disconnect never holds the index lock over network I/O.
func (i *fileConnectionIndex) closeGrant(reg Registration, owner CredentialOwner) error {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	key := fileCredentialKey(reg, owner)
	connections := i.grants[key]
	delete(i.grants, key)
	retired := make([]*fileConnection, 0, len(connections))
	for conn := range connections {
		retired = append(retired, conn)
	}
	i.mu.Unlock()
	return closeFileConnections(retired)
}

// NewFileSession creates a session connection owner. The service may be nil
// for tests that install a connector with SetConnectForTesting.
func NewFileSession(svc *Service) *FileSession {
	ctx, cancel := context.WithCancel(context.Background())
	s := &FileSession{svc: svc, ctx: ctx, cancel: cancel, connections: make(map[string]*fileConnection)}
	if svc != nil {
		s.connect = svc.connectFileSession
	}
	return s
}

// SetConnectForTesting replaces the session connector. The dirty callback
// must be retained by a fake that models tools/list_changed.
func (s *FileSession) SetConnectForTesting(fn func(context.Context, Registration, CredentialOwner, func()) (RemoteClient, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connect = fn
}

// Prepare retires dirty, removed, or replaced targets before a new turn. A
// changed endpoint/authentication target gets a new physical key while the
// logical file key lets us close its obsolete connection here.
func (s *FileSession) Prepare(ctx context.Context, registrations []Registration, authority authz.Authority) error {
	if s == nil {
		return errors.New("mcp: nil file session")
	}
	if !authority.Valid() {
		return authz.ErrForbidden
	}
	desired := make(map[string]string, len(registrations))
	for _, reg := range registrations {
		owner, err := FileCredentialOwner(reg, authority)
		if err != nil {
			return err
		}
		key, err := fileConnectionKey(reg, owner)
		if err != nil {
			return err
		}
		desired[key] = fileLogicalKey(reg)
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("mcp: file session is closed")
	}
	var retired []*fileConnection
	for key, conn := range s.connections {
		logical, selected := desired[key]
		if selected && logical == conn.logical && !conn.dirtyState() {
			continue
		}
		// A logical target can only have one selected endpoint/authorization
		// identity in a turn. Old keys are removed before new ones connect.
		delete(s.connections, key)
		s.removeIndexedConnection(conn)
		retired = append(retired, conn)
	}
	s.mu.Unlock()
	// Retired SDK sessions are terminal even when their remote DELETE fails;
	// that best-effort error must not prevent the next turn from connecting.
	_ = closeFileConnections(retired)
	return nil
}

func (s *FileSession) borrow(ctx context.Context, reg Registration, authority authz.Authority) (*fileConnection, error) {
	if s == nil {
		return nil, errors.New("mcp: nil file session")
	}
	owner, err := FileCredentialOwner(reg, authority)
	if err != nil {
		return nil, err
	}
	key, err := fileConnectionKey(reg, owner)
	if err != nil {
		return nil, err
	}
	logical := fileLogicalKey(reg)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("mcp: file session is closed")
	}
	// Dirty is a next-turn signal. A notification during discovery or a tool
	// call must not swap the handle underneath other proxies in this turn.
	if conn := s.connections[key]; conn != nil && !conn.closedState() {
		s.mu.Unlock()
		return conn, nil
	}
	var retired []*fileConnection
	for oldKey, conn := range s.connections {
		if oldKey == key || conn.logical == logical {
			delete(s.connections, oldKey)
			s.removeIndexedConnection(conn)
			retired = append(retired, conn)
		}
	}
	connector := s.connect
	s.mu.Unlock()
	_ = closeFileConnections(retired)
	if connector == nil {
		return nil, errors.New("mcp: file session has no connector")
	}
	// Keep request values while detaching the connection from caller
	// cancellation after initialization. The two bridges cover the only
	// cancellation points: caller cancellation during connect and session
	// cancellation for the lifetime of the live remote stream.
	connectCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stopCaller := context.AfterFunc(ctx, cancel)
	stopSession := context.AfterFunc(s.ctx, cancel)
	cleanupConnect := func() {
		stopCaller()
		stopSession()
		cancel()
	}
	newConn := &fileConnection{key: key, grant: fileCredentialKey(reg, owner), logical: logical, cancel: cleanupConnect}
	// Register before dialing so an explicit disconnect racing initialization
	// closes this handle and the post-connect check cannot resurrect it.
	s.registerConnection(reg, owner, newConn)
	client, err := connector(connectCtx, reg, owner, newConn.markDirty)
	if err != nil {
		s.removeIndexedConnection(newConn)
		_ = newConn.close()
		return nil, err
	}
	newConn.mu.Lock()
	newConn.client = client
	closed := newConn.closed
	newConn.mu.Unlock()
	if closed {
		s.removeIndexedConnection(newConn)
		cleanupConnect()
		_ = client.Close()
		return nil, errFileMCPGrantRevoked
	}
	// AfterFunc's stop result is the cancellation/connect race gate. A false
	// result means the caller cancellation callback already won or is running;
	// never publish a connection whose long-lived context it may still cancel.
	if !stopCaller() || ctx.Err() != nil {
		s.removeIndexedConnection(newConn)
		_ = newConn.close()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, context.Canceled
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.removeIndexedConnection(newConn)
		_ = newConn.close()
		return nil, errors.New("mcp: file session is closed")
	}
	// A concurrent turn may have installed the same key while this connect
	// was in flight. Keep the first owner and clean the duplicate locally.
	if existing := s.connections[key]; existing != nil && !existing.closedState() {
		s.mu.Unlock()
		s.removeIndexedConnection(newConn)
		_ = newConn.close()
		return existing, nil
	}
	s.connections[key] = newConn
	s.mu.Unlock()
	return newConn, nil
}

func (c *fileConnection) get() (RemoteClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.client == nil {
		return nil, errors.New("mcp: file connection is closed")
	}
	return c.client, nil
}

func (c *fileConnection) dirtyState() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dirty || c.closed
}

func (c *fileConnection) closedState() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *fileConnection) markDirty() {
	c.mu.Lock()
	c.dirty = true
	c.mu.Unlock()
}

func (c *fileConnection) close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	client := c.client
	c.client = nil
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if client == nil {
		return nil
	}
	// Client.Close is terminal even when SDK remote DELETE/TokenSource cleanup
	// fails. The handle is already removed from this session at this point.
	return client.Close()
}

func closeFileConnections(connections []*fileConnection) error {
	var errs []error
	for _, conn := range connections {
		if err := conn.close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Close terminates every connection once. Local cleanup is considered done
// even when a remote session's best-effort close reports an error.
func (s *FileSession) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	connections := make([]*fileConnection, 0, len(s.connections))
	for key, conn := range s.connections {
		delete(s.connections, key)
		s.removeIndexedConnection(conn)
		connections = append(connections, conn)
	}
	s.mu.Unlock()
	return closeFileConnections(connections)
}

func (s *FileSession) registerConnection(reg Registration, owner CredentialOwner, conn *fileConnection) {
	if s != nil && s.svc != nil {
		s.svc.fileConnections.add(reg, owner, conn)
	}
}

func (s *FileSession) removeIndexedConnection(conn *fileConnection) {
	if s == nil || s.svc == nil || conn == nil {
		return
	}
	s.svc.fileConnections.removeKey(conn.grant, conn)
}

// fileConnectionKey includes the registration's deterministic identity and
// trusted owner. Hashing keeps endpoint/authentication details out of logs.
func fileConnectionKey(reg Registration, owner CredentialOwner) (string, error) {
	if !reg.IsFile() || reg.ID == "" {
		return "", errors.New("mcp: file registration has no deterministic identity")
	}
	identity := reg.ID
	data := strings.Join([]string{identity, owner.Scope, owner.UserID, owner.AgentID}, "\x00")
	digest := sha256.Sum256([]byte(data))
	return hex.EncodeToString(digest[:]), nil
}

func fileLogicalKey(reg Registration) string {
	if reg.IsFile() {
		return strings.Join([]string{string(reg.FileKey.Scope), reg.FileKey.UserID, reg.FileKey.AgentID, string(reg.FileKey.Kind), reg.FileKey.Name, reg.ServerKey}, "\x00")
	}
	return ""
}

func fileToolPackageIdentity(reg Registration) string {
	digest := sha256.Sum256([]byte(fileLogicalKey(reg)))
	return "file-" + hex.EncodeToString(digest[:6])
}

// connectFileSession is the file-resource path. It installs a dirty callback
// instead of scheduling a database probe, so tools/list_changed only affects
// the next turn's disposable catalog.
func (s *Service) connectFileSession(ctx context.Context, reg Registration, owner CredentialOwner, dirty func()) (RemoteClient, error) {
	if s != nil && s.fileConnect != nil {
		return s.fileConnect(ctx, reg, owner, dirty)
	}
	transport, err := s.buildTransport(ctx, reg, owner)
	if err != nil {
		return nil, connectionError(reg, err)
	}
	c := mcpsdk.NewClient(clientImpl, &mcpsdk.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcpsdk.ToolListChangedRequest) {
			dirty()
		},
	})
	session, err := c.Connect(ctx, transport, nil)
	if err != nil {
		return nil, connectionError(reg, err)
	}
	return &Client{session: session}, nil
}

func (s *Service) buildFileTransport(ctx context.Context, reg Registration, owner CredentialOwner) (mcpsdk.Transport, error) {
	if err := validateFileOwner(reg, owner); err != nil {
		return nil, err
	}
	switch reg.AuthType {
	case AuthTypeNone:
		return buildDynamicBearerTransport(reg, nil, s.endpoints)
	case AuthTypeBearer:
		return buildDynamicBearerTransport(reg, func(requestCtx context.Context) (string, error) {
			snapshot, err := s.loadFileCredentialSnapshot(requestCtx, reg, owner)
			if err != nil {
				return "", err
			}
			if snapshot.BearerToken == "" {
				return "", errFileMCPGrantRevoked
			}
			return snapshot.BearerToken, nil
		}, s.endpoints)
	case AuthTypeOAuth:
		if reg.Transport != TransportStreamableHTTP {
			return nil, fmt.Errorf("mcp: auth_type %q requires the streamable_http transport", AuthTypeOAuth)
		}
		if err := s.endpoints.validateEndpointURL(reg.URL); err != nil {
			return nil, err
		}
		return &mcpsdk.StreamableClientTransport{
			Endpoint: reg.URL, HTTPClient: safeHTTPClientWithBearerFunc(nil, reg.Headers, s.endpoints),
			OAuthHandler: &oauthSession{svc: s, reg: reg, owner: owner},
		}, nil
	default:
		return nil, fmt.Errorf("mcp: invalid file auth type %q", reg.AuthType)
	}
}

// Compile-time checks keep accidental narrowing of the transport seam visible.
var (
	_ RemoteClient = (*Client)(nil)
	_ tools.Tool   = (*toolProxy)(nil)
)
